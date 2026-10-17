// SPDX-License-Identifier: GPL-2.0-only

// Command qamatrix maps the Apple TV QA matrix (docs/qa/atv-matrix.md) to
// real library items on a live Deadfin server, records the server's
// PlaybackInfo decision for each tvOS client profile, checks the streams
// at the container level, and merges AVFoundation probe results.
//
//	QAMATRIX_TOKEN=... go run ./tools/qamatrix map    -server https://deadfin.example
//	QAMATRIX_TOKEN=... go run ./tools/qamatrix check
//	                   go run ./tools/qamatrix plan   > plan.json
//	                   go run ./tools/qamatrix merge  -probe results.json
//	                   go run ./tools/qamatrix render
//	                   go run ./tools/qamatrix items  -cells V23
//
// map writes the library items to the local matrix file, which is not
// committed. Every command saves the public view without them; see Matrix.
//
// The API key comes from QAMATRIX_TOKEN or -token-file (- for stdin). The
// tool never writes it: URLs in the matrix have their credentials removed
// and errors pass through a redactor.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"sort"
	"strings"
	"time"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "qamatrix:", err)
		os.Exit(1)
	}
}

const usage = "usage: qamatrix map|check|plan|merge|render|items [flags]"

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New(usage)
	}
	switch args[0] {
	case "map":
		return cmdMap(ctx, args[1:], stderr)
	case "check":
		return cmdCheck(ctx, args[1:], stderr)
	case "plan":
		return cmdPlan(ctx, args[1:], stdout)
	case "merge":
		return cmdMerge(args[1:], stderr)
	case "render":
		return cmdRender(args[1:])
	case "items":
		return cmdItems(args[1:], stdout)
	}
	return errors.New(usage)
}

type common struct {
	matrix    string
	public    string
	server    string
	tokenFile string
	timeout   time.Duration
}

func (o *common) bind(fs *flag.FlagSet) {
	local := os.Getenv("QAMATRIX_LOCAL")
	if local == "" {
		local = "docs/qa/atv-matrix.local.json"
	}
	fs.StringVar(&o.matrix, "matrix", local, "local matrix JSON with the library items (not committed); default $QAMATRIX_LOCAL")
	fs.StringVar(&o.public, "public", "docs/qa/atv-matrix.json", "public matrix JSON without the library items")
	fs.StringVar(&o.server, "server", "", "Deadfin base URL (required)")
	fs.StringVar(&o.tokenFile, "token-file", "", "file with the API key (- for stdin); default $QAMATRIX_TOKEN")
	fs.DurationVar(&o.timeout, "timeout", 3*time.Minute, "per-request timeout")
}

func (o *common) save(m *Matrix) error { return saveMatrix(o.matrix, o.public, m) }

// loadAny reads the local matrix, or the public one when there is no local
// file.
func (o *common) loadAny() (*Matrix, error) {
	m, err := loadMatrix(o.matrix)
	if errors.Is(err, os.ErrNotExist) {
		return loadMatrix(o.public)
	}
	return m, err
}

func (o *common) client() (*client, error) {
	if o.server == "" {
		return nil, errors.New("-server is required")
	}
	tok := strings.TrimSpace(os.Getenv("QAMATRIX_TOKEN"))
	if o.tokenFile != "" {
		var b []byte
		var err error
		if o.tokenFile == "-" {
			b, err = io.ReadAll(io.LimitReader(os.Stdin, 4096))
		} else {
			b, err = os.ReadFile(o.tokenFile)
		}
		if err != nil {
			return nil, fmt.Errorf("read token: %w", err)
		}
		tok = strings.TrimSpace(string(b))
	}
	if tok == "" {
		return nil, errors.New("no API key: set QAMATRIX_TOKEN or -token-file")
	}
	return newClient(o.server, tok, o.timeout), nil
}

func cmdMap(ctx context.Context, args []string, log io.Writer) error {
	fs := flag.NewFlagSet("map", flag.ContinueOnError)
	var o common
	o.bind(fs)
	user := fs.String("user", "", "user id for PlaybackInfo (default: first administrator)")
	perCell := fs.Int("items", 2, "items per cell")
	only := fs.String("cells", "", "comma-separated cell ids to map; other cells keep their items and results from the local matrix (default: all)")
	cache := fs.String("library-cache", "", "read or write the fetched item list here")
	var pins pinFlag
	fs.Var(&pins, "pin", "CELL=ITEMID: map a pinned (regression) cell to this item; repeatable. Default: the local matrix's items")
	if err := fs.Parse(args); err != nil {
		return err
	}
	c, err := o.client()
	if err != nil {
		return err
	}
	var info struct{ Version string }
	if err := c.getJSON(ctx, "/System/Info/Public", &info); err != nil {
		return err
	}
	// /deadfin/v1/build names the Deadfin release; a stock server lacks it.
	var build struct{ Version, Revision string }
	_ = c.getJSON(ctx, "/deadfin/v1/build", &build)
	userID := *user
	if userID == "" {
		if userID, err = firstAdmin(ctx, c); err != nil {
			return err
		}
	}
	items, err := library(ctx, c, *cache)
	if err != nil {
		return err
	}
	fmt.Fprintf(log, "library: %d items\n", len(items))

	old, _ := o.loadAny()
	m := &Matrix{
		Schema: 1, Generated: time.Now().UTC().Format(time.RFC3339), Version: info.Version, Build: strings.TrimSpace(build.Version + " " + build.Revision),
		Assumptions: []string{
			"Apple TV 4K (2nd/3rd gen) on an HDR TV: HEVC, HDR10, HLG and Dolby Vision decode; no AV1 or VP9 decode.",
			"Client settings at defaults: Compatibility Auto, no forced HDR/DV transcode, no forced subtitle burn-in, no maximum resolution.",
			"Bitrate fixed at 120 Mbit/s (the clients' bitrate test on a LAN). Lower bitrates add transcodes that this matrix does not cover.",
			"Stock Swiftfin = SharkParty main before the subtitle overlay (844ca958). Its default player is VLC, whose profile equals SharkParty VLC.",
			"Remux = HLS fMP4 with the video copied; Transcode = HLS with the video encoded (QSV on live).",
		},
	}
	for _, p := range players() {
		m.Players = append(m.Players, PlayerInfo{Key: p, Title: playerTitle(p), AVPlayer: usesAVPlayer(p)})
	}
	want := idSet(*only)
	if len(want) > 0 && old != nil && old.Version != "" {
		// The results of the other cells came from the old build.
		m.Version, m.Build = old.Version, old.Build
	}
	for _, cl := range cells() {
		out := CellOut{
			ID: cl.ID, Group: cl.Group, Title: cl.Title, Dims: cl.Dims, Risk: cl.Risk, Pass: cl.Pass,
			Human: cl.Human, Minutes: cl.Minutes, Entries: cl.Entries, Expected: cl.Expect,
		}
		if len(want) > 0 && !want[cl.ID] {
			if oc := oldCell(old, cl.ID); oc != nil {
				out.Items, out.Missing, out.Operator = oc.Items, oc.Missing, oc.Operator
			}
			m.Cells = append(m.Cells, out)
			continue
		}
		pinned := pins[cl.ID]
		if old != nil {
			for _, oc := range old.Cells {
				if oc.ID == cl.ID {
					out.Operator = oc.Operator
					if cl.pinned && len(pinned) == 0 {
						pinned = map[string]bool{}
						for _, it := range oc.Items {
							pinned[it.ID] = true
						}
					}
				}
			}
		}
		if cl.pinned && len(pinned) > 0 {
			cl.match = byID(pinned)
		}
		for _, pick := range selectItems(cl, items, *perCell) {
			it, ms := pick.it, pick.ms
			sel := selection{MediaSourceID: ms.ID, AudioIndex: -1, SubtitleIndex: -1}
			if cl.audio != nil {
				sel.AudioIndex = cl.audio(ms)
			}
			if cl.subtitle != nil {
				sel.SubtitleIndex = cl.subtitle(ms)
			}
			row := ItemOut{
				ID: it.ID, Name: displayName(it), Summary: summarize(ms), MediaSourceID: ms.ID,
				RuntimeMin: int(it.RunTimeTicks / 600_000_000), AudioIndex: sel.AudioIndex, SubtitleIndex: sel.SubtitleIndex,
				Decisions: map[string]Decision{}, Mismatch: map[string]string{},
			}
			for _, p := range players() {
				d := playbackInfo(ctx, c, userID, p, it, sel)
				row.Decisions[p] = d
				if why := cl.Expect[p].mismatch(d); why != "" {
					row.Mismatch[p] = why
				}
			}
			if len(row.Mismatch) == 0 {
				row.Mismatch = nil
			}
			out.Items = append(out.Items, row)
		}
		if len(out.Items) == 0 {
			out.Missing = cl.Missing
			if out.Missing == "" {
				out.Missing = "Operator to source a sample."
			}
		}
		fmt.Fprintf(log, "%s: %d items\n", cl.ID, len(out.Items))
		m.Cells = append(m.Cells, out)
	}
	return o.save(m)
}

// oldCell returns the cell with this id in m, or nil.
func oldCell(m *Matrix, id string) *CellOut {
	if m == nil {
		return nil
	}
	for i := range m.Cells {
		if m.Cells[i].ID == id {
			return &m.Cells[i]
		}
	}
	return nil
}

func displayName(it item) string {
	if it.Type == "Episode" && it.SeriesName != "" {
		s, e := 0, 0
		if it.ParentIndexNumber != nil {
			s = *it.ParentIndexNumber
		}
		if it.IndexNumber != nil {
			e = *it.IndexNumber
		}
		return fmt.Sprintf("%s S%02dE%02d %s", it.SeriesName, s, e, it.Name)
	}
	return it.Name
}

func firstAdmin(ctx context.Context, c *client) (string, error) {
	var users []struct {
		ID     string `json:"Id"`
		Policy struct {
			IsAdministrator bool
		}
	}
	if err := c.getJSON(ctx, "/Users", &users); err != nil {
		return "", err
	}
	for _, u := range users {
		if u.Policy.IsAdministrator {
			return u.ID, nil
		}
	}
	return "", errors.New("no administrator user")
}

func library(ctx context.Context, c *client, cache string) ([]item, error) {
	if cache != "" {
		if b, err := os.ReadFile(cache); err == nil {
			var items []item
			if err := json.Unmarshal(b, &items); err == nil {
				return items, nil
			}
		}
	}
	var all []item
	for start := 0; ; start += 1000 {
		var page struct {
			Items            []item
			TotalRecordCount int
		}
		q := url.Values{
			"Recursive": {"true"}, "IncludeItemTypes": {"Movie,Episode"}, "Fields": {"MediaSources,Path"},
			"StartIndex": {fmt.Sprint(start)}, "Limit": {"1000"},
		}
		if err := c.getJSON(ctx, "/Items?"+q.Encode(), &page); err != nil {
			return nil, err
		}
		all = append(all, page.Items...)
		if len(page.Items) == 0 || len(all) >= page.TotalRecordCount {
			break
		}
	}
	if cache != "" {
		b, err := json.Marshal(all)
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(cache, b, 0o600); err != nil {
			return nil, err
		}
	}
	return all, nil
}

type pick struct {
	it item
	ms mediaSource
}

// selectItems picks up to n items for a cell: long enough for the probe,
// from different series or movies, in a stable order.
func selectItems(cl cell, items []item, n int) []pick {
	var cands []pick
	for _, it := range items {
		for _, ms := range it.MediaSources {
			if cl.match(it, ms) {
				cands = append(cands, pick{it, ms})
				break
			}
		}
	}
	minTicks := int64(cl.Minutes+3) * 600_000_000
	sort.SliceStable(cands, func(i, j int) bool {
		li, lj := cands[i].it.RunTimeTicks >= minTicks, cands[j].it.RunTimeTicks >= minTicks
		if li != lj {
			return li
		}
		return cands[i].it.ID < cands[j].it.ID
	})
	var out []pick
	seen := map[string]bool{}
	for _, p := range cands {
		key := p.it.SeriesName
		if key == "" {
			key = p.it.ID
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, p)
		if len(out) == n {
			break
		}
	}
	return out
}

func cmdCheck(ctx context.Context, args []string, log io.Writer) error {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	var o common
	o.bind(fs)
	runLen := fs.Int("segments", 6, "segments per run (one run from the start, one after a mid-file seek)")
	only := fs.String("cells", "", "comma-separated cell ids (default: all)")
	trace := fs.Bool("trace", false, "print each segment's track spans")
	user := fs.String("user", "", "user id for PlaybackInfo (default: first administrator)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	opt := checkOptions{runLen: *runLen}
	if *trace {
		opt.trace = log
	}
	c, err := o.client()
	if err != nil {
		return err
	}
	m, err := loadMatrix(o.matrix)
	if err != nil {
		return err
	}
	userID := *user
	if userID == "" {
		if userID, err = firstAdmin(ctx, c); err != nil {
			return err
		}
	}
	want := idSet(*only)
	entries := map[string][]float64{}
	for _, cl := range cells() {
		entries[cl.ID] = cl.Entries
	}
	for ci := range m.Cells {
		cell := &m.Cells[ci]
		if len(want) > 0 && !want[cell.ID] {
			continue
		}
		opt.entries = entries[cell.ID]
		for ii := range cell.Items {
			it := &cell.Items[ii]
			it.Checks = map[string]*Check{}
			done := map[string]*Check{}
			for _, p := range players() {
				d := it.Decisions[p]
				if d.StreamURL != "" {
					d = freshDecision(ctx, c, userID, p, *it)
				}
				key := streamKey(d)
				if k, ok := done[key]; ok && key != "" {
					it.Checks[p] = k
					continue
				}
				k := runCheck(ctx, c, p, d, opt)
				done[key] = k
				it.Checks[p] = k
				fmt.Fprintf(log, "%s %s %s: %s %s\n", cell.ID, it.ID, p, k.Result, strings.Join(k.Notes, "; "))
			}
		}
		if err := o.save(m); err != nil {
			return err
		}
	}
	return nil
}

// streamKey identifies a stream regardless of the device and play session,
// so that players that get the same stream share one check.
func streamKey(d Decision) string {
	if d.StreamURL == "" {
		return ""
	}
	u, err := url.Parse(d.StreamURL)
	if err != nil {
		return d.StreamURL
	}
	q := u.Query()
	q.Del("DeviceId")
	q.Del("PlaySessionId")
	q.Del("playSessionId")
	u.RawQuery = q.Encode()
	return d.Method + " " + u.String() + " " + d.SubtitleURL
}

func idSet(csv string) map[string]bool {
	out := map[string]bool{}
	for _, s := range strings.Split(csv, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out[s] = true
		}
	}
	return out
}

// PlanEntry is one avprobe run. URL has no credentials; avprobe adds the
// key from its stdin.
type PlanEntry struct {
	Cell          string    `json:"cell"`
	Item          string    `json:"item"`
	Player        string    `json:"player"`
	Name          string    `json:"name"`
	Method        string    `json:"method"`
	URL           string    `json:"url"`
	DeviceID      string    `json:"deviceId"`
	PlaySessionID string    `json:"playSessionId"`
	Seconds       float64   `json:"seconds"`
	Seeks         []float64 `json:"seeks"`
	RuntimeS      float64   `json:"runtimeS"`
}

func cmdPlan(ctx context.Context, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("plan", flag.ContinueOnError)
	var o common
	o.bind(fs)
	only := fs.String("cells", "", "comma-separated cell ids (default: all)")
	scale := fs.Float64("scale", 1, "multiply each cell's probe minutes")
	perCell := fs.Int("items", 0, "probe at most this many items per cell (0: all)")
	user := fs.String("user", "", "user id for PlaybackInfo (default: first administrator)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	c, err := o.client()
	if err != nil {
		return err
	}
	m, err := loadMatrix(o.matrix)
	if err != nil {
		return err
	}
	userID := *user
	if userID == "" {
		if userID, err = firstAdmin(ctx, c); err != nil {
			return err
		}
	}
	want := idSet(*only)
	var plan []PlanEntry
	for _, cell := range m.Cells {
		if len(want) > 0 && !want[cell.ID] {
			continue
		}
		for i, it := range cell.Items {
			if *perCell > 0 && i >= *perCell {
				break
			}
			seen := map[string]bool{}
			for _, p := range players() {
				d := it.Decisions[p]
				// AVFoundation on macOS models the AVPlayer clients only, and
				// cannot open MKV or AVI direct play.
				if !usesAVPlayer(p) || d.StreamURL == "" {
					continue
				}
				// Each plan entry gets its own play session, apart from the
				// mapped one and from a concurrent qamatrix check.
				if d = freshDecision(ctx, c, userID, p, it); d.StreamURL == "" {
					continue
				}
				key := streamKey(d)
				if seen[key] {
					continue
				}
				seen[key] = true
				secs := float64(cell.Minutes) * 60 * *scale
				rt := float64(it.RuntimeMin) * 60
				plan = append(plan, PlanEntry{
					Cell: cell.ID, Item: it.ID, Player: p, Name: it.Name, Method: d.Method,
					URL: strings.TrimRight(o.server, "/") + d.StreamURL, DeviceID: deviceID(p), PlaySessionID: d.PlaySessionID,
					Seconds: secs, RuntimeS: rt, Seeks: seekPlan(secs, rt),
				})
			}
		}
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(plan)
}

// seekPlan spreads the operations over the play time as offsets relative
// to the play head: +10 s, -10 s, +5 min, -5 min. A 5 min jump needs room
// in the runtime.
func seekPlan(secs, runtime float64) []float64 {
	seeks := []float64{10, -10}
	if runtime > secs+600 {
		seeks = append(seeks, 300, -300)
	}
	return seeks
}

func cmdMerge(args []string, log io.Writer) error {
	fs := flag.NewFlagSet("merge", flag.ContinueOnError)
	var o common
	o.bind(fs)
	probe := fs.String("probe", "", "avprobe results JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	m, err := loadMatrix(o.matrix)
	if err != nil {
		return err
	}
	b, err := os.ReadFile(*probe)
	if err != nil {
		return err
	}
	var results []struct {
		Cell   string `json:"cell"`
		Item   string `json:"item"`
		Player string `json:"player"`
		ProbeResult
	}
	if err := json.Unmarshal(b, &results); err != nil {
		return fmt.Errorf("%s: %w", *probe, err)
	}
	n := 0
	for _, r := range results {
		for ci := range m.Cells {
			if m.Cells[ci].ID != r.Cell {
				continue
			}
			for ii := range m.Cells[ci].Items {
				it := &m.Cells[ci].Items[ii]
				if it.ID != r.Item {
					continue
				}
				if it.Probe == nil {
					it.Probe = map[string]*ProbeResult{}
				}
				pr := r.ProbeResult
				it.Probe[r.Player] = &pr
				n++
			}
		}
	}
	fmt.Fprintf(log, "merged %d probe results\n", n)
	return o.save(m)
}

// cmdItems prints the local item mapping, for the operator's TV checks.
func cmdItems(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("items", flag.ContinueOnError)
	var o common
	o.bind(fs)
	only := fs.String("cells", "", "comma-separated cell ids (default: all)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	m, err := loadMatrix(o.matrix)
	if err != nil {
		return err
	}
	want := idSet(*only)
	for _, c := range m.Cells {
		if len(want) > 0 && !want[c.ID] {
			continue
		}
		for _, it := range c.Items {
			fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\n", c.ID, it.ID, it.Name, it.Summary)
		}
	}
	return nil
}

// pinFlag collects -pin CELL=ITEMID values.
type pinFlag map[string]map[string]bool

func (p *pinFlag) String() string { return "" }

func (p *pinFlag) Set(v string) error {
	cell, id, ok := strings.Cut(v, "=")
	if !ok || cell == "" || id == "" {
		return fmt.Errorf("want CELL=ITEMID, got %q", v)
	}
	if *p == nil {
		*p = pinFlag{}
	}
	if (*p)[cell] == nil {
		(*p)[cell] = map[string]bool{}
	}
	(*p)[cell][id] = true
	return nil
}

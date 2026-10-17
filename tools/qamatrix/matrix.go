// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
)

// Matrix is the QA matrix: cell -> item -> expected decision -> actual
// decision -> checks -> probe -> operator check.
//
// The tool keeps two files. The local file (default
// docs/qa/atv-matrix.local.json, not committed) holds the full matrix with
// the library items of the server under test. The public file
// (docs/qa/atv-matrix.json) holds the same cells with each item replaced by
// an anonymous run: the media summary, the decisions and the results, with
// no item IDs, names or URLs.
type Matrix struct {
	Schema      int          `json:"schema"`
	Generated   string       `json:"generated"`
	Version     string       `json:"serverVersion,omitempty"`
	Build       string       `json:"deadfinBuild,omitempty"`
	Assumptions []string     `json:"assumptions"`
	Players     []PlayerInfo `json:"players"`
	Cells       []CellOut    `json:"cells"`
}

type PlayerInfo struct {
	Key      string `json:"key"`
	Title    string `json:"title"`
	AVPlayer bool   `json:"avplayer"`
}

type CellOut struct {
	ID       string            `json:"id"`
	Group    string            `json:"group"`
	Title    string            `json:"title"`
	Dims     map[string]string `json:"dims"`
	Risk     string            `json:"risk"`
	Pass     string            `json:"pass"`
	Human    string            `json:"humanCheck,omitempty"`
	Minutes  int               `json:"probeMinutes"`
	Entries  []float64         `json:"entriesS,omitempty"`
	Expected map[string]Expect `json:"expected"`
	Items    []ItemOut         `json:"items,omitempty"`
	// Runs is the public view of Items.
	Runs []Run `json:"runs,omitempty"`
	// Missing is set when the library has no item for the cell.
	Missing string `json:"missing,omitempty"`
	// Status is the auto-probe roll-up: pass, fail, review, pending, no-item.
	Status   string   `json:"status"`
	Operator Operator `json:"operator"`
}

// Operator is filled in by hand on the Apple TV.
type Operator struct {
	Status string `json:"status"` // "", pass, fail, n/a
	Notes  string `json:"notes,omitempty"`
	By     string `json:"by,omitempty"`
	Date   string `json:"date,omitempty"`
}

type ItemOut struct {
	ID            string                  `json:"id"`
	Name          string                  `json:"name"`
	Summary       string                  `json:"summary"`
	MediaSourceID string                  `json:"mediaSourceId"`
	RuntimeMin    int                     `json:"runtimeMin"`
	AudioIndex    int                     `json:"audioIndex"`
	SubtitleIndex int                     `json:"subtitleIndex"`
	Decisions     map[string]Decision     `json:"decisions"`
	Mismatch      map[string]string       `json:"mismatch,omitempty"`
	Checks        map[string]*Check       `json:"checks,omitempty"`
	Probe         map[string]*ProbeResult `json:"probe,omitempty"`
}

// ProbeResult is one avprobe run (tools/qamatrix/avprobe) on macOS
// AVFoundation.
type ProbeResult struct {
	Result    string           `json:"result"`
	Notes     []string         `json:"notes,omitempty"`
	Metrics   map[string]any   `json:"metrics,omitempty"`
	AccessLog []map[string]any `json:"accessLog,omitempty"`
	ErrorLog  []map[string]any `json:"errorLog,omitempty"`
	Host      string           `json:"host,omitempty"`
	When      string           `json:"when,omitempty"`
}

func loadMatrix(path string) (*Matrix, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m Matrix
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &m, nil
}

// saveMatrix writes the full matrix to local and its public view to
// public.
func saveMatrix(local, public string, m *Matrix) error {
	for i := range m.Cells {
		m.Cells[i].Status = cellStatus(m.Cells[i])
	}
	scrub(m)
	if err := writeJSON(local, m); err != nil {
		return err
	}
	return writeJSON(public, publicView(m))
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

// Run is one mapped item without its identity.
type Run struct {
	Source     string                 `json:"source"`
	RuntimeMin int                    `json:"runtimeMin"`
	Decisions  map[string]RunDecision `json:"decisions"`
	Mismatch   map[string]string      `json:"mismatch,omitempty"`
	Checks     map[string]RunResult   `json:"checks,omitempty"`
	Probe      map[string]RunResult   `json:"probe,omitempty"`
}

type RunDecision struct {
	Method           string   `json:"method"`
	Audio            string   `json:"audio,omitempty"`
	AudioCodec       string   `json:"audioCodec,omitempty"`
	Subtitle         string   `json:"subtitle,omitempty"`
	TranscodeReasons []string `json:"transcodeReasons,omitempty"`
	Error            string   `json:"error,omitempty"`
}

type RunResult struct {
	Result string   `json:"result"`
	Notes  []string `json:"notes,omitempty"`
	TTFF   float64  `json:"ttffS,omitempty"`
}

// publicView returns a copy of m with each item replaced by a Run. Item
// IDs and names are removed from every note.
func publicView(m *Matrix) *Matrix {
	out := *m
	out.Cells = make([]CellOut, len(m.Cells))
	for ci, c := range m.Cells {
		if len(c.Items) > 0 {
			c.Runs = nil
			for _, it := range c.Items {
				c.Runs = append(c.Runs, runOf(it))
			}
		}
		c.Items = nil
		out.Cells[ci] = c
	}
	return &out
}

func runOf(it ItemOut) Run {
	red := func(s string) string { return redactItem(s, it) }
	r := Run{Source: it.Summary, RuntimeMin: it.RuntimeMin, Decisions: map[string]RunDecision{}}
	for p, d := range it.Decisions {
		r.Decisions[p] = RunDecision{Method: d.Method, Audio: d.Audio, AudioCodec: d.AudioCodec, Subtitle: d.Subtitle,
			TranscodeReasons: d.TranscodeReasons, Error: red(d.Error)}
	}
	for p, why := range it.Mismatch {
		if r.Mismatch == nil {
			r.Mismatch = map[string]string{}
		}
		r.Mismatch[p] = red(why)
	}
	for p, k := range it.Checks {
		if r.Checks == nil {
			r.Checks = map[string]RunResult{}
		}
		r.Checks[p] = RunResult{Result: k.Result, Notes: redactAll(k.Notes, red)}
	}
	for p, pr := range it.Probe {
		if r.Probe == nil {
			r.Probe = map[string]RunResult{}
		}
		res := RunResult{Result: pr.Result, Notes: redactAll(pr.Notes, red)}
		if v, ok := pr.Metrics["ttffS"].(float64); ok {
			res.TTFF = v
		}
		r.Probe[p] = res
	}
	return r
}

func redactAll(v []string, f func(string) string) []string {
	var out []string
	for _, s := range v {
		out = append(out, f(s))
	}
	return out
}

var (
	idPattern  = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-?[0-9a-f]{4}-?[0-9a-f]{4}-?[0-9a-f]{4}-?[0-9a-f]{12}\b`)
	urlPattern = regexp.MustCompile(`https?://[^\s"']+`)
)

// redactItem removes URLs, item and media source IDs, and the item name
// from s.
func redactItem(s string, it ItemOut) string {
	s = urlPattern.ReplaceAllString(s, "<url>")
	s = idPattern.ReplaceAllString(s, "<id>")
	if it.Name != "" {
		s = strings.ReplaceAll(s, it.Name, "<item>")
	}
	if len(it.ID) >= 8 {
		s = strings.ReplaceAll(s, it.ID[:8], "<id>")
	}
	return s
}

// scrub keeps server names, host names and play sessions out of the
// committed matrix. Probe log URIs keep only their path, stream URLs lose
// their PlaySessionId, and the probe host is dropped. Each run posts a fresh
// PlaybackInfo, so nothing reads the stored sessions back.
func scrub(m *Matrix) {
	for ci := range m.Cells {
		for ii := range m.Cells[ci].Items {
			it := &m.Cells[ci].Items[ii]
			for p, d := range it.Decisions {
				d.PlaySessionID = ""
				d.StreamURL = dropQuery(d.StreamURL, "playsessionid")
				d.SubtitleURL = dropQuery(d.SubtitleURL, "playsessionid")
				it.Decisions[p] = d
			}
			for _, r := range it.Probe {
				r.Host = ""
				for _, logs := range [][]map[string]any{r.AccessLog, r.ErrorLog} {
					for _, e := range logs {
						if u, ok := e["uri"].(string); ok {
							e["uri"] = pathOnly(u)
						}
					}
				}
			}
		}
	}
}

func dropQuery(raw, key string) string {
	u, err := url.Parse(raw)
	if err != nil || u.RawQuery == "" {
		return raw
	}
	q := u.Query()
	for k := range q {
		if strings.EqualFold(k, key) {
			q.Del(k)
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func pathOnly(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.EscapedPath()
}

// cellStatus rolls up checks and probes. A failed check or probe fails
// the cell. A decision that differs from the expectation needs review: it
// is either a defect or a wrong expectation.
func cellStatus(c CellOut) string {
	if len(c.Items) == 0 {
		return "no-item"
	}
	status := "pass"
	pending := false
	for _, it := range c.Items {
		for _, k := range it.Checks {
			if k.Result == "fail" {
				return "fail"
			}
		}
		for _, p := range it.Probe {
			if p.Result == "fail" {
				return "fail"
			}
		}
		if len(it.Mismatch) > 0 {
			status = "review"
		}
		if len(it.Checks) == 0 {
			pending = true
		}
	}
	if pending && status == "pass" {
		return "pending"
	}
	return status
}

func summarize(ms mediaSource) string {
	var parts []string
	parts = append(parts, strings.ToUpper(strings.Split(ms.Container, ",")[0]))
	if v := videoOf(ms); v != nil {
		parts = append(parts, fmt.Sprintf("%s %s %d-bit %s %dp %.3f", v.Codec, v.Profile, v.BitDepth, v.VideoRangeType, v.Height, v.RealFrameRate))
	}
	var audio []string
	for _, a := range streamsOf(ms, "Audio") {
		s := fmt.Sprintf("%s %dch", a.Codec, a.Channels)
		if a.Language != "" {
			s += " " + a.Language
		}
		audio = append(audio, s)
	}
	if len(audio) > 3 {
		audio = append(audio[:3], fmt.Sprintf("+%d", len(audio)-3))
	}
	if len(audio) > 0 {
		parts = append(parts, "audio "+strings.Join(audio, ", "))
	}
	counts := map[string]int{}
	for _, s := range streamsOf(ms, "Subtitle") {
		counts[strings.ToLower(s.Codec)]++
	}
	if len(counts) > 0 {
		keys := make([]string, 0, len(counts))
		for k := range counts {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var subs []string
		for _, k := range keys {
			subs = append(subs, fmt.Sprintf("%dx %s", counts[k], k))
		}
		parts = append(parts, "subs "+strings.Join(subs, ", "))
	}
	if n := len(ms.MediaAttachments); n > 0 {
		parts = append(parts, fmt.Sprintf("%d attachments", n))
	}
	return strings.Join(parts, "; ")
}

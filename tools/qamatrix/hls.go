// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Check is the server-side stream check for one item and player: what an
// HLS client would fetch, verified at the container level. For HLS it
// fetches the playlists, the init segment and two runs of media segments
// (from the start, and after a seek to mid-file) and checks that every
// track's decode timeline is continuous. That is the defect class where a
// segment carried the media of the previous one.
type Check struct {
	Result string   `json:"result"` // pass, fail, skip
	Notes  []string `json:"notes,omitempty"`
	// HLS facts.
	TargetDuration float64           `json:"targetDuration,omitempty"`
	Segments       int               `json:"segments,omitempty"`
	SampleEntries  map[string]string `json:"sampleEntries,omitempty"` // handler -> fourcc
	MaxGapMs       map[string]int    `json:"maxGapMs,omitempty"`      // handler -> worst |discontinuity|
	PlaylistDrift  float64           `json:"playlistDriftS,omitempty"`
	FirstSegmentS  float64           `json:"firstSegmentS,omitempty"` // time to first media segment
	// Direct play facts.
	HTTPStatus  int    `json:"httpStatus,omitempty"`
	ContentType string `json:"contentType,omitempty"`
	// Subtitle delivery.
	SubtitleBytes int `json:"subtitleBytes,omitempty"`
}

func (k *Check) fail(format string, a ...any) {
	k.Result = "fail"
	k.Notes = append(k.Notes, fmt.Sprintf(format, a...))
}

func (k *Check) note(format string, a ...any) {
	k.Notes = append(k.Notes, fmt.Sprintf(format, a...))
}

type checkOptions struct {
	runLen int       // segments per run
	trace  io.Writer // per-segment spans, when set
	// entries are extra start points in seconds, as a resume or a seek
	// sends them: a negative value counts from the end. Each starts its
	// own run at the segment that holds that time.
	entries []float64
}

// runCheck checks one decision's stream and subtitle delivery.
func runCheck(ctx context.Context, c *client, player string, d Decision, opt checkOptions) *Check {
	k := &Check{Result: "pass"}
	if d.Error != "" || d.StreamURL == "" {
		k.Result = "skip"
		k.note("no stream: %s", d.Error)
		return k
	}
	switch d.Method {
	case "Remux", "Transcode":
		checkHLS(ctx, c, player, d, opt, k)
		stopEncoding(ctx, c, player, d.PlaySessionID)
	case "DirectPlay":
		checkDirect(ctx, c, player, d, k)
	default:
		k.Result = "skip"
		k.note("method %s not checked", d.Method)
	}
	if d.SubtitleURL != "" {
		checkSubtitle(ctx, c, player, d, k)
	}
	return k
}

func stopEncoding(ctx context.Context, c *client, player, playSession string) {
	if playSession == "" {
		return
	}
	v := url.Values{"deviceId": {deviceID(player)}, "playSessionId": {playSession}}
	_, _, _ = c.do(ctx, http.MethodDelete, "/Videos/ActiveEncodings?"+v.Encode(), deviceID(player), nil, nil)
}

func checkDirect(ctx context.Context, c *client, player string, d Decision, k *Check) {
	hdr := http.Header{"Range": {"bytes=0-1048575"}}
	b, h, err := c.do(ctx, http.MethodGet, d.StreamURL, deviceID(player), nil, hdr)
	if err != nil {
		k.fail("direct stream: %v", err)
		return
	}
	k.ContentType = h.Get("Content-Type")
	if h.Get("Content-Range") == "" {
		k.fail("direct stream ignores Range (no Content-Range); AVPlayer requires byte ranges")
	}
	k.HTTPStatus = http.StatusPartialContent
	if len(b) == 0 {
		k.fail("direct stream returned no bytes")
	}
}

func checkSubtitle(ctx context.Context, c *client, player string, d Decision, k *Check) {
	b, _, err := c.do(ctx, http.MethodGet, d.SubtitleURL, deviceID(player), nil, nil)
	if err != nil {
		k.fail("subtitle delivery: %v", err)
		return
	}
	k.SubtitleBytes = len(b)
	lower := strings.ToLower(d.SubtitleURL)
	switch {
	case len(b) == 0:
		k.fail("subtitle delivery returned no bytes")
	case strings.Contains(lower, "stream.ass") || strings.Contains(lower, "stream.ssa"):
		if !bytes.Contains(b[:min(len(b), 4096)], []byte("[Script Info]")) {
			k.fail("ASS delivery has no [Script Info] header")
		}
	case strings.Contains(lower, "stream.srt"):
		if !bytes.Contains(b, []byte("-->")) {
			k.fail("SRT delivery has no cue timings")
		}
	case strings.Contains(lower, "stream.vtt"):
		if !bytes.HasPrefix(bytes.TrimPrefix(b, []byte("\xef\xbb\xbf")), []byte("WEBVTT")) {
			k.fail("WebVTT delivery has no WEBVTT header")
		}
	case strings.Contains(lower, "stream.sup") || strings.Contains(lower, "stream.pgssub"):
		if !bytes.HasPrefix(b, []byte("PG")) {
			k.fail("PGS delivery does not start with a PG segment")
		}
	}
}

type mediaPlaylist struct {
	initURI   string
	segments  []string
	durations []float64
	target    float64
}

func parsePlaylist(b []byte) (mediaPlaylist, []string) {
	var p mediaPlaylist
	var variants []string
	var pending float64
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == "":
		case strings.HasPrefix(line, "#EXT-X-MAP:"):
			if i := strings.Index(line, `URI="`); i >= 0 {
				rest := line[i+5:]
				if j := strings.IndexByte(rest, '"'); j >= 0 {
					p.initURI = rest[:j]
				}
			}
		case strings.HasPrefix(line, "#EXT-X-TARGETDURATION:"):
			p.target, _ = strconv.ParseFloat(strings.TrimPrefix(line, "#EXT-X-TARGETDURATION:"), 64)
		case strings.HasPrefix(line, "#EXTINF:"):
			v := strings.TrimPrefix(line, "#EXTINF:")
			if i := strings.IndexByte(v, ','); i >= 0 {
				v = v[:i]
			}
			pending, _ = strconv.ParseFloat(v, 64)
		case strings.HasPrefix(line, "#"):
		default:
			if pending > 0 {
				p.segments = append(p.segments, line)
				p.durations = append(p.durations, pending)
				pending = 0
			} else {
				variants = append(variants, line)
			}
		}
	}
	return p, variants
}

func resolveRef(base, ref string) string {
	b, err := url.Parse(base)
	if err != nil {
		return ref
	}
	r, err := url.Parse(ref)
	if err != nil {
		return ref
	}
	return b.ResolveReference(r).String()
}

func checkHLS(ctx context.Context, c *client, player string, d Decision, opt checkOptions, k *Check) {
	dev := deviceID(player)
	master := c.resolve(d.StreamURL)
	b, _, err := c.do(ctx, http.MethodGet, master, dev, nil, nil)
	if err != nil {
		k.fail("master playlist: %v", err)
		return
	}
	_, variants := parsePlaylist(b)
	if len(variants) == 0 {
		k.fail("master playlist has no variant")
		return
	}
	variant := resolveRef(master, variants[0])
	b, _, err = c.do(ctx, http.MethodGet, variant, dev, nil, nil)
	if err != nil {
		k.fail("media playlist: %v", err)
		return
	}
	pl, _ := parsePlaylist(b)
	k.TargetDuration = pl.target
	k.Segments = len(pl.segments)
	if len(pl.segments) == 0 {
		k.fail("media playlist has no segments")
		return
	}
	if pl.initURI == "" {
		k.fail("media playlist has no EXT-X-MAP: not fMP4")
		return
	}
	ib, _, err := c.do(ctx, http.MethodGet, resolveRef(variant, pl.initURI), dev, nil, nil)
	if err != nil {
		k.fail("init segment: %v", err)
		return
	}
	tracks, err := parseInit(ib)
	if err != nil {
		k.fail("init segment: %v", err)
		return
	}
	k.SampleEntries = map[string]string{}
	for _, t := range tracks {
		k.SampleEntries[t.Handler] = t.SampleEntry
	}
	if se := k.SampleEntries["vide"]; usesAVPlayer(player) && se == "hev1" {
		k.fail("video sample entry hev1: AVPlayer plays HEVC in HLS only as hvc1/dvh1")
	}

	// Playlist start times, to compare media against.
	starts := make([]float64, len(pl.durations))
	for i := 1; i < len(starts); i++ {
		starts[i] = starts[i-1] + pl.durations[i-1]
	}
	k.MaxGapMs = map[string]int{}
	var origin map[uint32]float64

	run := func(from int, label string) {
		to := min(from+opt.runLen, len(pl.segments))
		var prev map[uint32]span
		for i := from; i < to; i++ {
			t0 := time.Now()
			sb, _, err := c.do(ctx, http.MethodGet, resolveRef(variant, pl.segments[i]), dev, nil, nil)
			if err != nil {
				k.fail("%s segment %d: %v", label, i, err)
				return
			}
			if i == 0 {
				k.FirstSegmentS = round3(time.Since(t0).Seconds())
			}
			spans, err := parseSegment(sb, tracks)
			if err != nil {
				k.fail("%s segment %d: %v", label, i, err)
				return
			}
			if origin == nil {
				origin = map[uint32]float64{}
				for id, s := range spans {
					origin[id] = s.Start
				}
			}
			for id, s := range spans {
				h := tracks[id].Handler
				if s.End <= s.Start {
					k.fail("%s segment %d: %s track has no duration", label, i, h)
				}
				if prev != nil {
					if p, ok := prev[id]; ok {
						gap := s.Start - p.End
						ms := int(math.Round(math.Abs(gap) * 1000))
						if ms > k.MaxGapMs[h] {
							k.MaxGapMs[h] = ms
						}
						if math.Abs(gap) > gapTolerance {
							k.fail("%s segment %d: %s timeline %s of %.3f s at %.3f s", label, i, h, gapWord(gap), math.Abs(gap), p.End)
						}
					}
				}
				if o, ok := origin[id]; ok && h == "vide" {
					drift := (s.Start - o) - starts[i]
					if math.Abs(drift) > math.Abs(k.PlaylistDrift) {
						k.PlaylistDrift = round3(drift)
					}
					if math.Abs(drift) > math.Max(pl.target, 1) {
						// A video remux of a file without keyframe data gets
						// equal-length segments, and ffmpeg stream copy cuts
						// at source keyframes, so upstream drifts the same way
						// (DynamicHlsPlaylistGenerator.cs:37-46). A transcode
						// forces keyframes on the segment grid and must not drift.
						if d.Method == "Remux" {
							k.note("%s segment %d: video starts %.3f s from its playlist time (remux drift, upstream parity)", label, i, drift)
						} else {
							k.fail("%s segment %d: video starts %.3f s from its playlist time", label, i, drift)
						}
					}
				}
			}
			if opt.trace != nil {
				var ids []uint32
				for id := range spans {
					ids = append(ids, id)
				}
				sort.Slice(ids, func(a, b int) bool { return ids[a] < ids[b] })
				fmt.Fprintf(opt.trace, "  %s seg %d playlist %.3f+%.3f %d bytes:", label, i, starts[i], pl.durations[i], len(sb))
				for _, id := range ids {
					s := spans[id]
					fmt.Fprintf(opt.trace, " %s %.3f-%.3f (%d)", tracks[id].Handler, s.Start, s.End, s.Samples)
				}
				fmt.Fprintln(opt.trace)
			}
			prev = spans
		}
	}
	run(0, "start")
	if mid := len(pl.segments) / 2; mid > opt.runLen {
		run(mid, "after seek")
	}
	total := starts[len(starts)-1] + pl.durations[len(pl.durations)-1]
	for _, at := range opt.entries {
		if at < 0 {
			at += total
		}
		if i := segmentAt(starts, at); i >= 0 {
			run(i, fmt.Sprintf("entry %.0f s", at))
		}
	}
}

// segmentAt returns the index of the segment that holds time t, or -1 when
// t is outside the playlist.
func segmentAt(starts []float64, t float64) int {
	if t < 0 || len(starts) == 0 {
		return -1
	}
	return sort.SearchFloat64s(starts, t+1e-9) - 1
}

// gapTolerance is the largest decode-time step between segments, in
// seconds, that is not a discontinuity: about one video frame at 24 fps or
// two AAC frames.
const gapTolerance = 0.050

func gapWord(gap float64) string {
	if gap > 0 {
		return "gap"
	}
	return "overlap"
}

func round3(v float64) float64 { return math.Round(v*1000) / 1000 }

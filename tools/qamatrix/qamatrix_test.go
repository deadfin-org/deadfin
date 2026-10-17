// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Box builders for synthetic fMP4.

func mkbox(typ string, payload ...[]byte) []byte {
	n := 8
	for _, p := range payload {
		n += len(p)
	}
	b := make([]byte, 8, n)
	binary.BigEndian.PutUint32(b, uint32(n))
	copy(b[4:], typ)
	for _, p := range payload {
		b = append(b, p...)
	}
	return b
}

func u32(v ...uint32) []byte {
	b := make([]byte, 4*len(v))
	for i, x := range v {
		binary.BigEndian.PutUint32(b[4*i:], x)
	}
	return b
}

func u64(v uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, v)
	return b
}

func trak(id, timescale uint32, handler, entry string) []byte {
	tkhd := mkbox("tkhd", u32(0, 0, 0, id, 0, 0)) // v0: flags, ctime, mtime, id, reserved, duration
	mdhd := mkbox("mdhd", u32(0, 0, 0, timescale, 0, 0))
	hdlr := mkbox("hdlr", u32(0, 0), []byte(handler), u32(0, 0, 0), []byte{0})
	stsd := mkbox("stsd", u32(0, 1), mkbox(entry, make([]byte, 8)))
	return mkbox("trak", tkhd, mkbox("mdia", mdhd, hdlr, mkbox("minf", mkbox("stbl", stsd))))
}

func initSegment(videoEntry string) []byte {
	return mkbox("moov",
		trak(1, 90000, "vide", videoEntry),
		trak(2, 48000, "soun", "mp4a"),
		mkbox("mvex", mkbox("trex", u32(0, 1, 1, 3750, 0, 0)), mkbox("trex", u32(0, 2, 1, 1024, 0, 0))),
	)
}

// traf with an explicit per-sample duration table.
func traf(id uint32, base uint64, durations ...uint32) []byte {
	tfhd := mkbox("tfhd", u32(0, id))
	tfdt := mkbox("tfdt", u32(1<<24), u64(base))
	trun := mkbox("trun", u32(0x100, uint32(len(durations))), u32(durations...))
	return mkbox("traf", tfhd, tfdt, trun)
}

// segment builds one media segment of n seconds starting at start seconds.
func segment(start, n float64) []byte {
	var vd, ad []uint32
	for i := 0; i < int(n*24); i++ {
		vd = append(vd, 3750)
	}
	for i := 0; i < int(n*48000/1024); i++ {
		ad = append(ad, 1024)
	}
	return append(mkbox("moof", traf(1, uint64(start*90000), vd...), traf(2, uint64(start*48000), ad...)), mkbox("mdat")...)
}

func TestParseInitAndSegment(t *testing.T) {
	tracks, err := parseInit(initSegment("hvc1"))
	if err != nil {
		t.Fatal(err)
	}
	if v := tracks[1]; v.Handler != "vide" || v.Timescale != 90000 || v.SampleEntry != "hvc1" || v.DefaultDuration != 3750 {
		t.Fatalf("video track = %+v", v)
	}
	if a := tracks[2]; a.Handler != "soun" || a.Timescale != 48000 || a.SampleEntry != "mp4a" {
		t.Fatalf("audio track = %+v", a)
	}
	spans, err := parseSegment(segment(6, 6), tracks)
	if err != nil {
		t.Fatal(err)
	}
	if v := spans[1]; v.Start != 6 || v.End != 12 || v.Samples != 144 {
		t.Fatalf("video span = %+v", v)
	}
	if a := spans[2]; a.Start != 6 || a.Samples != 281 {
		t.Fatalf("audio span = %+v", a)
	}
}

func TestParseSegmentNegativeBase(t *testing.T) {
	tracks, err := parseInit(initSegment("avc1"))
	if err != nil {
		t.Fatal(err)
	}
	// ffmpeg -copyts gives transcoded audio a negative base for encoder
	// priming: here -1024 samples.
	seg := append(mkbox("moof", traf(2, uint64(1<<64-1024), 1024, 1024)), mkbox("mdat")...)
	spans, err := parseSegment(seg, tracks)
	if err != nil {
		t.Fatal(err)
	}
	if a := spans[2]; a.Start != -1024.0/48000 || a.End != 1024.0/48000 {
		t.Fatalf("audio span = %+v", a)
	}
}

func TestTrunDefaultDuration(t *testing.T) {
	trun := u32(0, 10) // no per-sample durations
	d, n, err := trunDuration(trun, 1024)
	if err != nil || d != 10240 || n != 10 {
		t.Fatalf("trunDuration = %d, %d, %v", d, n, err)
	}
	if _, _, err := trunDuration(trun, 0); err == nil {
		t.Fatal("want error without any duration")
	}
}

func TestParsePlaylist(t *testing.T) {
	pl, variants := parsePlaylist([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:6\n#EXT-X-MAP:URI=\"init.mp4?a=1\"\n#EXTINF:6.006,\nseg0.mp4\n#EXTINF:5.5,\nseg1.mp4\n#EXT-X-ENDLIST\n"))
	if len(variants) != 0 || pl.initURI != "init.mp4?a=1" || pl.target != 6 || len(pl.segments) != 2 || pl.durations[1] != 5.5 {
		t.Fatalf("playlist = %+v, variants %v", pl, variants)
	}
	_, variants = parsePlaylist([]byte("#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\nmain.m3u8?x=1\n"))
	if len(variants) != 1 || variants[0] != "main.m3u8?x=1" {
		t.Fatalf("variants = %v", variants)
	}
}

// hlsServer serves a fixed fMP4 rendition. bad makes segment 1 repeat the
// media of segment 0, as a copy-remux defect did. short makes each segment hold 3 s
// of media under a 6 s EXTINF, as a keyframe-cut remux does.
func hlsServer(t *testing.T, entry string, bad, short bool) *httptest.Server {
	t.Helper()
	const n = 8
	mux := http.NewServeMux()
	mux.HandleFunc("/videos/x/master.m3u8", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\nmain.m3u8?"+r.URL.RawQuery+"\n")
	})
	mux.HandleFunc("/videos/x/main.m3u8", func(w http.ResponseWriter, r *http.Request) {
		var sb strings.Builder
		sb.WriteString("#EXTM3U\n#EXT-X-TARGETDURATION:6\n#EXT-X-MAP:URI=\"init.mp4\"\n")
		for i := 0; i < n; i++ {
			fmt.Fprintf(&sb, "#EXTINF:6.0,\nseg/%d.mp4\n", i)
		}
		fmt.Fprint(w, sb.String())
	})
	mux.HandleFunc("/videos/x/init.mp4", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(initSegment(entry)) })
	mux.HandleFunc("/videos/x/seg/", func(w http.ResponseWriter, r *http.Request) {
		var i int
		fmt.Sscanf(strings.TrimPrefix(r.URL.Path, "/videos/x/seg/"), "%d.mp4", &i)
		start, n := float64(i)*6, 6.0
		if bad && i == 1 {
			start = 0
		}
		if short {
			start, n = float64(i)*3, 3
		}
		_, _ = w.Write(segment(start, n))
	})
	mux.HandleFunc("/Videos/ActiveEncodings", func(w http.ResponseWriter, r *http.Request) {})
	return httptest.NewServer(mux)
}

func TestCheckHLS(t *testing.T) {
	for _, tc := range []struct {
		name   string
		entry  string
		bad    bool
		short  bool
		method string
		want   string
		note   string
	}{
		{name: "continuous", entry: "hvc1", want: "pass"},
		{name: "repeated segment", entry: "hvc1", bad: true, want: "fail", note: "overlap of 6.000 s"},
		{name: "hev1 sample entry", entry: "hev1", want: "fail", note: "hev1"},
		{name: "remux drift is parity", entry: "hvc1", short: true, want: "pass", note: "remux drift"},
		{name: "transcode drift", entry: "hvc1", short: true, method: "Transcode", want: "fail", note: "from its playlist time"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := hlsServer(t, tc.entry, tc.bad, tc.short)
			defer srv.Close()
			c := newClient(srv.URL, "secret-token", 10*time.Second)
			method := tc.method
			if method == "" {
				method = "Remux"
			}
			d := Decision{Method: method, StreamURL: "/videos/x/master.m3u8?PlaySessionId=p", PlaySessionID: "p"}
			k := runCheck(context.Background(), c, playerNative, d, checkOptions{runLen: 3})
			if k.Result != tc.want {
				t.Fatalf("result %s, want %s: %v", k.Result, tc.want, k.Notes)
			}
			if tc.note != "" && !strings.Contains(strings.Join(k.Notes, "\n"), tc.note) {
				t.Fatalf("notes %v lack %q", k.Notes, tc.note)
			}
			if tc.want == "pass" && (k.MaxGapMs["vide"] != 0 || k.SampleEntries["soun"] != "mp4a") {
				t.Fatalf("check = %+v", k)
			}
		})
	}
}

func TestRedact(t *testing.T) {
	c := newClient("http://x", "tok123", time.Second)
	in := `Get "http://x/videos/a/main.m3u8?ApiKey=tok123&b=1&api_key=other": tok123 failed`
	got := c.redact(in)
	if strings.Contains(got, "tok123") || strings.Contains(got, "other") || !strings.Contains(got, "ApiKey=REDACTED&b=1") {
		t.Fatalf("redact = %q", got)
	}
	if s := stripSecrets("/videos/a/master.m3u8?ApiKey=tok123&DeviceId=d"); strings.Contains(s, "tok123") || !strings.Contains(s, "DeviceId=d") {
		t.Fatalf("stripSecrets = %q", s)
	}
}

func TestClassify(t *testing.T) {
	src := mediaSource{ID: "ms", MediaStreams: []mediaStream{
		{Type: "Video", Index: 0, Codec: "hevc"},
		{Type: "Audio", Index: 1, Codec: "aac", IsDefault: true},
		{Type: "Subtitle", Index: 2, Codec: "subrip", DeliveryMethod: "Hls"},
		{Type: "Subtitle", Index: 3, Codec: "ass", DeliveryMethod: "External", DeliveryURL: "/Videos/a/Subtitles/3/0/Stream.ass?ApiKey=tok"},
	}}
	hls := func(q string) playbackInfoResponse {
		s := src
		s.TranscodingURL = "/videos/a/master.m3u8?ApiKey=tok&AudioCodec=aac,ac3&VideoCodec=hevc,h264&" + q
		s.TranscodingProtocol = "hls"
		return playbackInfoResponse{MediaSources: []mediaSource{s}, PlaySessionID: "ps"}
	}
	c := newClient("http://x", "tok", time.Second)
	for _, tc := range []struct {
		name string
		resp playbackInfoResponse
		sub  int
		want Expect
	}{
		{name: "remux", resp: hls("TranscodeReasons=ContainerNotSupported,VideoCodecTagNotSupported"), sub: -1, want: ex(rx, cp, "")},
		{name: "audio transcode", resp: hls("TranscodeReasons=ContainerNotSupported,AudioCodecNotSupported"), sub: -1, want: ex(rx, at, "")},
		{name: "video transcode", resp: hls("TranscodeReasons=VideoCodecNotSupported"), sub: -1, want: ex(tc, cp, "")},
		{name: "hls subtitles keep the copy", resp: hls("SubtitleMethod=Hls&TranscodeReasons=SubtitleCodecNotSupported"), sub: 2, want: ex(rx, cp, "Hls")},
		{name: "burn-in transcodes", resp: hls("SubtitleMethod=Encode&TranscodeReasons=SubtitleCodecNotSupported"), sub: 2, want: ex(tc, cp, "Hls")},
		{name: "direct play", resp: playbackInfoResponse{MediaSources: []mediaSource{func() mediaSource { s := src; s.SupportsDirectPlay = true; return s }()}}, sub: 3, want: ex(dp, cp, "External")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := classify(c, item{ID: "a"}, selection{MediaSourceID: "ms", AudioIndex: -1, SubtitleIndex: tc.sub}, tc.resp)
			if why := tc.want.mismatch(d); why != "" {
				t.Fatalf("%s: %+v", why, d)
			}
			if strings.Contains(d.StreamURL+d.SubtitleURL, "tok") {
				t.Fatalf("credentials kept: %s %s", d.StreamURL, d.SubtitleURL)
			}
		})
	}
}

func TestCellsAreWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range cells() {
		if seen[c.ID] {
			t.Errorf("duplicate cell %s", c.ID)
		}
		seen[c.ID] = true
		if c.match == nil || c.Title == "" || c.Risk == "" || c.Pass == "" || c.Minutes <= 0 {
			t.Errorf("cell %s is incomplete", c.ID)
		}
		for _, p := range players() {
			if _, ok := c.Expect[p]; !ok {
				t.Errorf("cell %s has no expectation for %s", c.ID, p)
			}
		}
	}
}

func TestProfilesMarshal(t *testing.T) {
	for _, p := range players() {
		b, err := json.Marshal(profileFor(p))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), `"Protocol":"hls"`) || !strings.Contains(string(b), `"SubtitleProfiles"`) {
			t.Fatalf("%s profile = %s", p, b)
		}
	}
	// SharkParty's overlay takes PGS External; stock Swiftfin burns it in.
	has := func(p, format, method string) bool {
		for _, s := range profileFor(p).SubtitleProfiles {
			if s.Format == format && s.Method == method {
				return true
			}
		}
		return false
	}
	if !has(playerNative, "pgssub", "External") || !has(playerSwiftfin, "pgssub", "Encode") || has(playerSwiftfin, "pgssub", "External") {
		t.Fatal("PGS subtitle profiles do not match the clients")
	}
}

func TestSelectItemsPrefersLongDistinctItems(t *testing.T) {
	c := cell{Minutes: 3, match: func(it item, _ mediaSource) bool { return true }}
	long := int64(20) * 600_000_000
	items := []item{
		{ID: "a", SeriesName: "S", RunTimeTicks: long, MediaSources: []mediaSource{{}}},
		{ID: "b", SeriesName: "S", RunTimeTicks: long, MediaSources: []mediaSource{{}}},
		{ID: "c", RunTimeTicks: 600_000_000, MediaSources: []mediaSource{{}}},
		{ID: "d", RunTimeTicks: long, MediaSources: []mediaSource{{}}},
	}
	got := selectItems(c, items, 2)
	if len(got) != 2 || got[0].it.ID != "a" || got[1].it.ID != "d" {
		t.Fatalf("selected %v", got)
	}
}

func TestScrub(t *testing.T) {
	m := &Matrix{Cells: []CellOut{{Items: []ItemOut{{
		Decisions: map[string]Decision{"p": {StreamURL: "/videos/x/master.m3u8?DeviceId=d&PlaySessionId=s", PlaySessionID: "s"}},
		Probe:     map[string]*ProbeResult{"p": {Host: "host.local macOS 27", AccessLog: []map[string]any{{"uri": "https://server.example/videos/x/main.m3u8?ApiKey=k"}}}},
	}}}}}
	scrub(m)
	it := m.Cells[0].Items[0]
	if d := it.Decisions["p"]; d.PlaySessionID != "" || d.StreamURL != "/videos/x/master.m3u8?DeviceId=d" {
		t.Errorf("decision not scrubbed: %+v", d)
	}
	r := it.Probe["p"]
	if r.Host != "" || r.AccessLog[0]["uri"] != "/videos/x/main.m3u8" {
		t.Errorf("probe not scrubbed: %+v", r)
	}
}

func TestPublicView(t *testing.T) {
	id := "0123456789abcdef0123456789abcdef"
	m := &Matrix{Cells: []CellOut{{ID: "V01", Items: []ItemOut{{
		ID: id, Name: "Some Film", Summary: "MKV; hevc main",
		Decisions: map[string]Decision{"p": {Method: "Remux", StreamURL: "/videos/" + id + "/master.m3u8", Error: "GET https://server.example/x: 500"}},
		Checks:    map[string]*Check{"p": {Result: "fail", Notes: []string{"/videos/" + id + "/main.m3u8: Some Film timed out"}}},
		Probe:     map[string]*ProbeResult{"p": {Result: "pass", Metrics: map[string]any{"ttffS": 1.5}}},
	}}}}}
	pub := publicView(m)
	b, err := json.Marshal(pub)
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{id, id[:8], "Some Film", "server.example", "streamUrl"} {
		if strings.Contains(string(b), leak) {
			t.Errorf("public view holds %q: %s", leak, b)
		}
	}
	if len(m.Cells[0].Items) != 1 {
		t.Error("publicView changed the local matrix")
	}
	r := pub.Cells[0].Runs[0]
	if r.Source != "MKV; hevc main" || r.Decisions["p"].Method != "Remux" || r.Probe["p"].TTFF != 1.5 || r.Checks["p"].Result != "fail" {
		t.Errorf("run = %+v", r)
	}
}

func TestSegmentAt(t *testing.T) {
	starts := []float64{0, 6, 12, 18}
	for _, tc := range []struct {
		at   float64
		want int
	}{{0, 0}, {5.9, 0}, {6, 1}, {17.5, 2}, {178, 3}, {-1, -1}} {
		if got := segmentAt(starts, tc.at); got != tc.want {
			t.Errorf("segmentAt(%v) = %d, want %d", tc.at, got, tc.want)
		}
	}
}

func TestCheckHLSEntries(t *testing.T) {
	srv := hlsServer(t, "hvc1", false, false)
	defer srv.Close()
	c := newClient(srv.URL, "secret-token", 10*time.Second)
	d := Decision{Method: "Remux", StreamURL: "/videos/x/master.m3u8?PlaySessionId=p", PlaySessionID: "p"}
	var trace strings.Builder
	k := runCheck(context.Background(), c, playerNative, d, checkOptions{runLen: 2, trace: &trace, entries: []float64{25, -10}})
	if k.Result != "pass" {
		t.Fatalf("result %s: %v", k.Result, k.Notes)
	}
	// 25 s is in segment 4; -10 s from the 48 s end is 38 s, in segment 6.
	for _, want := range []string{"entry 25 s seg 4 ", "entry 38 s seg 6 ", "entry 38 s seg 7 "} {
		if !strings.Contains(trace.String(), want) {
			t.Errorf("trace lacks %q:\n%s", want, trace.String())
		}
	}
}

func TestInteractionCells(t *testing.T) {
	for _, c := range cells() {
		if c.Group == "Interaction" && (c.Dims["delivery"] == "" || c.Dims["ops"] == "" || c.Dims["player"] == "") {
			t.Errorf("interaction cell %s lacks delivery, ops or player", c.ID)
		}
		if c.Dims["delivery"] != "" && strings.Contains(c.Dims["ops"], "resume") && c.Dims["delivery"] != "DirectPlay" && len(c.Entries) == 0 {
			t.Errorf("HLS resume cell %s has no entry point for qamatrix check", c.ID)
		}
	}
}

func TestOperatorSteps(t *testing.T) {
	byID := map[string]cell{}
	for _, c := range cells() {
		byID[c.ID] = c
	}
	for id, want := range map[string][]string{
		"ATV-R03": {"Video player: Native", "Compatibility must be Auto", "press Back at about 2:58", "choose it", "expected delivery is Remux a:copy", "Method must read Remux"},
		"ATV-I15": {"Method must read Direct Stream"},
		"ATV-I32": {"Video player: VLC", "Method must read Transcode"},
		"ATV-I04": {"Scrub to 90:00"},
		"ATV-I36": {"Resume is blocked on rc.8", "Method must read Direct Play"},
	} {
		h := byID[id].Human
		for _, w := range want {
			if !strings.Contains(h, w) {
				t.Errorf("%s steps lack %q: %s", id, w, h)
			}
		}
	}
}

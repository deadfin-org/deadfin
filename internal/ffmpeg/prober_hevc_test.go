// SPDX-License-Identifier: GPL-2.0-only

package ffmpeg

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// hevcAttachmentProbe is a fake ffprobe for an HEVC MKV with an embedded
// font. Like ffprobe, it dumps the extradata of every selected stream for
// -show_data: the font attachment dump is 64 KiB, so a main probe with
// -show_data exceeds the 16 KiB output bound of these tests. The narrow
// run (-select_streams v) prints the hvcC record of the video stream, the
// first 32 bytes of an x265 high-tier encode.
const hevcAttachmentProbe = `
printf '%s\n' "$@" >> "$ARGV_FILE"
printf '%s\n' "--" >> "$ARGV_FILE"
case "$*" in
*-select_streams*)
	[ -z "${NARROW_FAIL:-}" ] || exit 1
	printf '{"programs":[],"streams":[{"index":0,"codec_name":"hevc","extradata":"\\n00000000: 0124 0800 0000 9e08 0000 0000 99f0 00fc  .$..............\\n00000010: fff8 f800 000f 0420 0001 0017 4001 0c01  ....... ....@...\\n"}]}'
	;;
*-show_data*)
	printf '{"streams":[{"index":0,"codec_name":"hevc","codec_type":"video","extradata":"'
	head -c 65536 /dev/zero | tr '\0' 'f'
	printf '"}]}'
	;;
*)
	printf '{"streams":[{"index":0,"codec_name":"%s","codec_type":"video"},{"index":1,"codec_type":"attachment","codec_name":"ttf","tags":{"filename":"font.ttf"}}],"format":{"format_name":"matroska,webm"}}' "${VIDEO_CODEC:-hevc}"
	;;
esac
`

// probeRuns splits the recorded argv file into one argv per ffprobe run.
func probeRuns(t *testing.T, path string) [][]string {
	t.Helper()
	var runs [][]string
	var run []string
	for _, line := range readLines(t, path) {
		if line == "--" {
			runs = append(runs, run)
			run = nil
			continue
		}
		run = append(run, line)
	}
	return runs
}

func TestProbeReadsHEVCExtradataInANarrowRun(t *testing.T) {
	argv := filepath.Join(t.TempDir(), "argv")
	t.Setenv("ARGV_FILE", argv)
	path := writeFakeFFProbe(t, hevcAttachmentProbe)

	got, err := (Prober{Path: path, Timeout: fixtureTimeout, MaxOutput: 16 << 10}).Probe(context.Background(), "/media/fonts.mkv")
	if err != nil {
		t.Fatalf("Probe(): %v", err)
	}
	runs := probeRuns(t, argv)
	if len(runs) != 2 {
		t.Fatalf("ffprobe runs = %q, want the main run and one narrow run", runs)
	}
	for _, arg := range runs[0] {
		if arg == "-show_data" {
			t.Fatalf("main ffprobe argv %q dumps every stream's extradata", runs[0])
		}
	}
	want := []string{"-v", "warning", "-print_format", "json", "-select_streams", "v", "-show_entries", "stream=index,codec_name,extradata", "-show_data", "-i", "/media/fonts.mkv"}
	if !reflect.DeepEqual(runs[1], want) {
		t.Fatalf("narrow ffprobe argv = %q\nwant %q", runs[1], want)
	}
	var result struct {
		Streams       []json.RawMessage `json:"streams"`
		HEVCExtradata map[string]string `json:"deadfin_hevc_extradata"`
	}
	if err := json.Unmarshal(got, &result); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, got)
	}
	if len(result.Streams) != 2 || !strings.Contains(result.HEVCExtradata["0"], "00000000: 0124 0800") {
		t.Fatalf("output = %s, want both streams and the stream 0 extradata", got)
	}
	if len(got) > 2048 {
		t.Fatalf("output is %d bytes, want the attachment data left out", len(got))
	}
}

func TestProbeSkipsTheNarrowRunWithoutHEVC(t *testing.T) {
	argv := filepath.Join(t.TempDir(), "argv")
	t.Setenv("ARGV_FILE", argv)
	t.Setenv("VIDEO_CODEC", "h264")
	path := writeFakeFFProbe(t, hevcAttachmentProbe)

	got, err := (Prober{Path: path, Timeout: fixtureTimeout, MaxOutput: 16 << 10}).Probe(context.Background(), "/media/fonts.mkv")
	if err != nil {
		t.Fatalf("Probe(): %v", err)
	}
	if runs := probeRuns(t, argv); len(runs) != 1 {
		t.Fatalf("ffprobe runs = %q, want the main run only", runs)
	}
	if strings.Contains(string(got), hevcExtradataKey) {
		t.Fatalf("output = %s, want no HEVC extradata", got)
	}
}

func TestProbeKeepsTheMainOutputWhenTheNarrowRunFails(t *testing.T) {
	argv := filepath.Join(t.TempDir(), "argv")
	t.Setenv("ARGV_FILE", argv)
	t.Setenv("NARROW_FAIL", "1")
	path := writeFakeFFProbe(t, hevcAttachmentProbe)

	got, err := (Prober{Path: path, Timeout: fixtureTimeout, MaxOutput: 16 << 10}).ProbeAndValidate(context.Background(), "/media/fonts.mkv")
	if err != nil {
		t.Fatalf("ProbeAndValidate(): %v", err)
	}
	if runs := probeRuns(t, argv); len(runs) != 2 {
		t.Fatalf("ffprobe runs = %q, want two", runs)
	}
	if strings.Contains(string(got), hevcExtradataKey) || !strings.Contains(string(got), `"attachment"`) {
		t.Fatalf("output = %s, want the main output unchanged", got)
	}
}

func TestProbeWithShowDataExceedsTheBoundOnAttachments(t *testing.T) {
	// The fake reproduces the failure that the narrow run avoids: -show_data
	// on the main run dumps the attachment and exceeds MaxOutput.
	t.Setenv("ARGV_FILE", filepath.Join(t.TempDir(), "argv"))
	path := writeFakeFFProbe(t, hevcAttachmentProbe)
	prober := Prober{Path: path, Timeout: fixtureTimeout, MaxOutput: 16 << 10}
	args := append(prober.args("/media/fonts.mkv"), "-show_data")
	_, err := prober.probeJSON(context.Background(), args, nil)
	if !errors.Is(err, ErrProbeOutputTooLarge) {
		t.Fatalf("probeJSON with -show_data: %v, want ErrProbeOutputTooLarge", err)
	}
}

func TestAppendProbeObject(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{`{"streams":[]}`, `{"streams":[],"k":{"0":"x"}}`},
		{" {} \n", `{"k":{"0":"x"}}`},
		{"{ }", `{ "k":{"0":"x"}}`},
	} {
		got, err := appendProbeObject([]byte(tc.raw), "k", map[string]string{"0": "x"})
		if err != nil || string(got) != tc.want {
			t.Errorf("appendProbeObject(%q) = %s, %v, want %s", tc.raw, got, err, tc.want)
		}
	}
	if _, err := appendProbeObject([]byte(`[]`), "k", 1); err == nil {
		t.Error("appendProbeObject accepted a JSON array")
	}
}

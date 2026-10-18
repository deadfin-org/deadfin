// SPDX-License-Identifier: GPL-2.0-only

package ffmpeg

import (
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/sourcefile"
)

func TestFirstPacketArgsBoundTheReadForEveryStream(t *testing.T) {
	got := firstPacketArgs("/dev/fd/3", 3*time.Second)
	want := []string{"-v", "error", "-read_intervals", "%+3", "-show_entries", "packet=stream_index,pts_time", "-of", "csv=p=0", "-i", "/dev/fd/3"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("firstPacketArgs = %q, want %q", got, want)
	}
}

func TestParseFirstPacketReturnsTheEarliestTimeOfTheStream(t *testing.T) {
	for _, test := range []struct {
		name   string
		output string
		stream int
		ticks  int64
		found  bool
	}{
		{name: "late track absent from window", output: "0,0.000000\n0,0.042000\n3,0.000000\n", stream: 1, found: false},
		{name: "on-time track", output: "0,0.000000\n1,0.032000\n1,0.000000\n", stream: 1, ticks: 0, found: true},
		{name: "reordered video", output: "0,0.334000\n0,0.167000\n0,N/A\n0,0.042000\n", stream: 0, ticks: 420_000, found: true},
		{name: "trailing separator", output: "1,1.500000,\n", stream: 1, ticks: 15_000_000, found: true},
		{name: "empty", output: "", stream: 1, found: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			ticks, found, err := parseFirstPacket([]byte(test.output), test.stream)
			if err != nil || ticks != test.ticks || found != test.found {
				t.Fatalf("parseFirstPacket(%q, %d) = %d, %t, %v; want %d, %t", test.output, test.stream, ticks, found, err, test.ticks, test.found)
			}
		})
	}
	for _, bad := range []string{"garbage\n", "x,1.0\n", "1,abc\n"} {
		if _, _, err := parseFirstPacket([]byte(bad), 1); err == nil {
			t.Errorf("parseFirstPacket(%q) error = nil", bad)
		}
	}
}

// The ATV-I18 shape: an AC3 track whose first packet is 33.55 s into the
// file. The probe must find it late without reading up to that packet.
func TestFirstPacketTicksFindsALateTrackWithRealFFprobe(t *testing.T) {
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	ffprobePath, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe is not installed")
	}
	media := filepath.Join(t.TempDir(), "late.mkv")
	// Stream 2 starts 8 s into the file.
	build := exec.CommandContext(t.Context(), ffmpegPath, "-nostdin", "-v", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=s=160x90:r=24:d=12",
		"-f", "lavfi", "-i", "sine=f=440:r=48000:d=12",
		"-itsoffset", "8", "-f", "lavfi", "-i", "sine=f=660:r=48000:d=4",
		"-map", "0", "-map", "1", "-map", "2", "-c:v", "mpeg4", "-c:a", "ac3",
		media)
	if output, err := build.CombinedOutput(); err != nil {
		t.Skipf("building the late-track fixture: %v: %s", err, output)
	}
	pinned, err := sourcefile.Open(media, sourcefile.Expected{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pinned.Close() }()
	prober := Prober{Path: ffprobePath, Timeout: 30 * time.Second}
	for _, test := range []struct {
		stream int
		found  bool
	}{
		{stream: 1, found: true},
		{stream: 2, found: false},
	} {
		ticks, found, err := prober.FirstPacketTicks(t.Context(), pinned, test.stream, 3*time.Second)
		if err != nil || found != test.found || (found && ticks > 1_000_000) {
			t.Errorf("FirstPacketTicks(stream %d) = %d, %t, %v; want found=%t near 0", test.stream, ticks, found, err, test.found)
		}
	}
}

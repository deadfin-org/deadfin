// SPDX-License-Identifier: GPL-2.0-only

package playback_test

import (
	"bytes"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/deadfin-org/deadfin/internal/hls"
	"github.com/deadfin-org/deadfin/internal/keyframes"
	"github.com/deadfin-org/deadfin/internal/playback"
)

// TestHLSKeyframeRemuxSegmentsMatchPlaylistAfterRestartWithRealFFmpeg runs the
// remux argv that Segment builds through real ffmpeg. The source is an MKV
// with irregular keyframes, and its keyframe plan comes from the Matroska
// Cues as on the live server. Started at the first segment and at two
// mid-file segments, every following segment body must hold exactly the
// video of its advertised interval (deadfin#629). Before the fix, ffmpeg cut
// on an hls_time grid from the restart keyframe and drifted off the playlist.
func TestHLSKeyframeRemuxSegmentsMatchPlaylistAfterRestartWithRealFFmpeg(t *testing.T) {
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skipf("requires ffmpeg: %v", err)
	}
	ffprobePath, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skipf("requires ffprobe: %v", err)
	}
	encoders, err := exec.CommandContext(t.Context(), ffmpegPath, "-hide_banner", "-encoders").Output()
	if err != nil || !bytes.Contains(encoders, []byte(" libx264 ")) {
		t.Skipf("requires ffmpeg with libx264: %v", err)
	}

	const durationSeconds = 240
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.mkv")
	var forced []string
	steps := []float64{0.5, 1.2, 2.1, 3.3, 4.2, 5.0, 6.4, 1.7, 2.9}
	for at, step := 0.0, 0; at < durationSeconds-1; step++ {
		forced = append(forced, strconv.FormatFloat(at, 'f', 3, 64))
		at += steps[step%len(steps)]
	}
	runRealFFmpeg(t, ffmpegPath, "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=size=160x90:rate=24000/1001",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
		"-t", strconv.Itoa(durationSeconds), "-map", "0:v", "-map", "1:a",
		"-c:v", "libx264", "-preset", "ultrafast", "-x264-params", "keyint=600:min-keyint=1:scenecut=0",
		"-force_key_frames", strings.Join(forced, ","), "-c:a", "aac", "-y", sourcePath)

	file, err := os.Open(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	info, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	data, err := keyframes.ExtractMatroska(t.Context(), file, info.Size())
	_ = file.Close()
	if err != nil {
		t.Fatalf("ExtractMatroska: %v", err)
	}

	source := progressiveCopySource(sourcePath)
	runtime := int64(durationSeconds) * 10_000_000
	source.RunTimeTicks = &runtime
	width, height := 160, 90
	source.MediaStreams[0].Width, source.MediaStreams[0].Height = &width, &height
	source.MediaStreams[1].Codec = "aac"
	transcodeDir := filepath.Join(dir, "transcodes")
	if err := os.MkdirAll(transcodeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	request := hlsTranscodeRequest()
	source.ID = request.MediaSourceID
	request.AllowVideoStreamCopy, request.AllowAudioStreamCopy, request.EnableAutoStreamCopy = true, true, true
	request.VideoBitrate = hlsInt(2_600_000)
	request.SegmentContainer = "mp4"
	request.SegmentLengthSeconds = 6
	keyframeSource := &hlsFakeKeyframes{data: data, found: true}
	manager := &hlsGOPManager{}
	config := hlsServiceConfig(&progressiveFakeSource{source: source}, manager)
	config.TranscodeDir = transcodeDir
	config.Keyframes = keyframeSource
	service := playback.NewHLSService(config)

	plan, found, err := hls.RemuxKeyframePlan(t.Context(), hls.MainPlaylistRequest{
		ItemID: request.ItemID, MediaSourceID: request.MediaSourceID, FilePath: sourcePath,
		SegmentLengthMilliseconds: 6_000, RunTimeTicks: runtime, IsRemuxVideo: true, Keyframes: keyframeSource,
	})
	if err != nil || !found {
		t.Fatalf("RemuxKeyframePlan() = found %v, error %v", found, err)
	}
	if len(plan.Segments) < 30 {
		t.Fatalf("plan has %d segments, want at least 30", len(plan.Segments))
	}

	for _, start := range []int{0, 9, 17} {
		t.Run(fmt.Sprintf("restart at segment %d", start), func(t *testing.T) {
			request.SegmentIndex = start
			request.RuntimeTicks = plan.Segments[start].StartTicks
			if _, err := service.Segment(t.Context(), request); err != nil {
				t.Fatalf("Segment(%d): %v", start, err)
			}
			job := manager.lastRestart
			entries, _ := os.ReadDir(transcodeDir)
			for _, entry := range entries {
				_ = os.Remove(filepath.Join(transcodeDir, entry.Name()))
			}
			runRealFFmpeg(t, ffmpegPath, append([]string{"-hide_banner", "-loglevel", "error", "-nostdin"}, job.Args...)...)

			init := strings.TrimSuffix(job.PlaylistPath, ".m3u8") + "-1.mp4"
			// ffmpeg builds differ in a constant start offset of the copied
			// timeline (about 21 ms with some AAC priming). A GOP off is at
			// least 0.5 s here, so the check takes the offset from the first
			// body and then requires every boundary to agree to 5 ms.
			checked, offset := 0, math.NaN()
			for index := start; index < len(plan.Segments); index++ {
				segment := plan.Segments[index]
				parts := []string{init}
				for gop := segment.FirstKeyframe; gop < segment.EndKeyframe; gop++ {
					parts = append(parts, fmt.Sprintf(job.SegmentPattern, gop))
				}
				first, last := probeVideoRange(t, ffprobePath, filepath.Join(dir, "joined.mp4"), parts)
				startSeconds := float64(segment.StartTicks) / 1e7
				endSeconds := float64(segment.StartTicks+segment.LengthTicks) / 1e7
				if math.IsNaN(offset) {
					offset = first - startSeconds
					if math.Abs(offset) > 0.1 {
						t.Fatalf("segment %d advertised %.3f-%.3f, body video %.3f-%.3f: timeline offset %.3f s", index, startSeconds, endSeconds, first, last, offset)
					}
				}
				if math.Abs(first-offset-startSeconds) > 0.005 || last-offset >= endSeconds {
					t.Errorf("segment %d advertised %.3f-%.3f, body video %.3f-%.3f (timeline offset %.3f s)", index, startSeconds, endSeconds, first, last, offset)
				}
				checked++
			}
			if checked < 10 {
				t.Fatalf("checked %d segments, want a long run", checked)
			}
		})
	}
}

func runRealFFmpeg(t *testing.T, path string, args ...string) {
	t.Helper()
	command := exec.CommandContext(t.Context(), path, args...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("%s %q: %v\n%s", path, args, err, output)
	}
}

// probeVideoRange joins the files and returns the first and last video
// presentation times in seconds.
func probeVideoRange(t *testing.T, ffprobePath, joined string, parts []string) (float64, float64) {
	t.Helper()
	var body []byte
	for _, part := range parts {
		data, err := os.ReadFile(part)
		if err != nil {
			t.Fatalf("segment file: %v", err)
		}
		body = append(body, data...)
	}
	if err := os.WriteFile(joined, body, 0o600); err != nil {
		t.Fatal(err)
	}
	output, err := exec.CommandContext(t.Context(), ffprobePath, "-v", "error", "-select_streams", "v",
		"-show_entries", "packet=pts_time", "-of", "csv=p=0", joined).Output()
	if err != nil {
		t.Fatalf("ffprobe: %v", err)
	}
	var times []float64
	for _, line := range strings.Fields(string(output)) {
		value, err := strconv.ParseFloat(strings.TrimSuffix(line, ","), 64)
		if err == nil {
			times = append(times, value)
		}
	}
	if len(times) == 0 {
		t.Fatalf("no video packets in %q", parts)
	}
	return slices.Min(times), slices.Max(times)
}

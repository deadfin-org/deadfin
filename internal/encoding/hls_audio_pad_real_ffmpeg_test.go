// SPDX-License-Identifier: GPL-2.0-only

package encoding

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestHLSPadsLateAudioWithRealFFmpeg reproduces QA cell ATV-I18: a stereo
// AC3 track whose first packet is 20 s after the video. A copy writes an
// init segment without the dac3 box (the mp4 muxer builds it from the first
// AC3 packet) and first segments without audio; AVPlayer rejects that
// stream. The padded AC3 encode writes dac3 and audio in segment 0 (D-82).
func TestHLSPadsLateAudioWithRealFFmpeg(t *testing.T) {
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skipf("real HLS audio pad test requires ffmpeg: %v", err)
	}
	encoders, err := exec.CommandContext(t.Context(), ffmpegPath, "-hide_banner", "-encoders").Output()
	if err != nil || !bytes.Contains(encoders, []byte(" libx264 ")) {
		t.Skipf("real HLS audio pad test requires ffmpeg with libx264: %v", err)
	}
	source := filepath.Join(t.TempDir(), "late.mkv")
	runFFmpeg(t, ffmpegPath,
		"-f", "lavfi", "-i", "testsrc2=size=160x90:rate=24:duration=30",
		"-itsoffset", "20", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=10",
		"-map", "0", "-map", "1", "-c:v", "libx264", "-preset", "ultrafast", "-g", "24",
		"-c:a", "ac3", "-ac", "2", "-b:a", "256k", "-y", source)

	for _, test := range []struct {
		name          string
		codec         string
		pad           bool
		wantDAC3      bool
		wantSegment0A bool
	}{
		{name: "copy", codec: "copy", wantDAC3: false, wantSegment0A: false},
		{name: "padded AC3 encode", codec: "ac3", pad: true, wantDAC3: true, wantSegment0A: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			job := hlsUnitJob()
			job.InputPath = source
			job.InputContainer = "matroska,webm"
			job.OutputPath = filepath.Join(dir, "main.m3u8")
			job.OutputContainer = "fmp4"
			job.OutputVideoCodec = "copy"
			job.VideoStream = VideoStream{Index: 0, Codec: "h264", NalLengthSize: "4"}
			job.SubtitleStream = nil
			job.AudioStream = AudioStream{Index: 1, Codec: "ac3", Channels: 2}
			job.OutputAudioCodec = test.codec
			job.OutputAudioBitrate = 256_000
			job.OutputAudioChannels = 2
			job.OutputAudioSampleRate = 48_000
			job.KeyframeCuts = true
			job.PadAudioStart = test.pad
			args, err := BuildHLSArgs(job, progressiveTestCapabilities())
			if err != nil {
				t.Fatal(err)
			}
			runFFmpeg(t, ffmpegPath, args...)

			init := readTestFile(t, filepath.Join(dir, "main-1.mp4"))
			if got := bytes.Contains(init, []byte("dac3")); got != test.wantDAC3 {
				t.Errorf("init segment has dac3 = %t, want %t", got, test.wantDAC3)
			}
			// One traf per track that has samples in the fragment.
			segment := readTestFile(t, filepath.Join(dir, "main0.mp4"))
			if got := bytes.Count(segment, []byte("traf")) == 2; got != test.wantSegment0A {
				t.Errorf("segment 0 has audio = %t, want %t", got, test.wantSegment0A)
			}
		})
	}
}

func readTestFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

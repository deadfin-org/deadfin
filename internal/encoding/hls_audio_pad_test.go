// SPDX-License-Identifier: GPL-2.0-only

package encoding

import (
	"strings"
	"testing"
)

// lateAudioHLSJob is the ATV-I18 remux: HEVC copy with a stereo AC3 track
// that starts 33.55 s into the film, into fMP4.
func lateAudioHLSJob(outputAudioCodec string) HLSJob {
	job := hlsUnitJob()
	job.OutputContainer = "fmp4"
	job.OutputVideoCodec = "copy"
	job.VideoStream.Codec = "hevc"
	job.SubtitleStream = nil
	job.AudioStream = AudioStream{Index: 1, Codec: "ac3", Channels: 2}
	job.OutputAudioCodec = outputAudioCodec
	job.OutputAudioBitrate = 256_000
	job.OutputAudioChannels = 2
	job.OutputAudioSampleRate = 48_000
	job.PadAudioStart = true
	return job
}

func TestBuildHLSArgsPadsLateAudioFromTheJobStart(t *testing.T) {
	for _, tc := range []struct {
		name, codec, wantEncoder string
		startTicks               int64
		sampleRate               int
		wantFilter               string
	}{
		{name: "AAC from zero", codec: "aac", wantEncoder: "aac_at", wantFilter: "aresample=osr=48000:async=1:first_pts=0"},
		{name: "AC3 restart", codec: "ac3", wantEncoder: "ac3", startTicks: 120_000_000, wantFilter: "aresample=osr=48000:async=1:first_pts=576000"},
		{name: "E-AC3 at 44.1 kHz", codec: "eac3", wantEncoder: "eac3", startTicks: 10_000_000, sampleRate: 44_100, wantFilter: "aresample=osr=44100:async=1:first_pts=44100"},
		{name: "no output rate", codec: "aac", wantEncoder: "aac_at", startTicks: 5_000_000, sampleRate: -1, wantFilter: "aresample=osr=48000:async=1:first_pts=24000"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			job := lateAudioHLSJob(tc.codec)
			job.StartOffsetTicks = tc.startTicks
			switch {
			case tc.sampleRate > 0:
				job.OutputAudioSampleRate = tc.sampleRate
			case tc.sampleRate < 0:
				job.OutputAudioSampleRate = 0
			}
			args := mustBuildHLSArgs(t, job)
			if got := args[argumentIndex(t, args, "-codec:a:0")+1]; got != tc.wantEncoder {
				t.Errorf("-codec:a:0 = %q, want %q; argv=%q", got, tc.wantEncoder, args)
			}
			if got := args[argumentIndex(t, args, "-af")+1]; got != tc.wantFilter {
				t.Errorf("-af = %q, want %q", got, tc.wantFilter)
			}
			if got := args[argumentIndex(t, args, "-max_interleave_delta")+1]; got != "0" {
				t.Errorf("-max_interleave_delta = %q, want 0", got)
			}
			// The pad needs the source timestamps; the copied video keeps them.
			if countArgument(args, "-copyts") != 1 || !strings.Contains(strings.Join(args, " "), "-codec:v:0 copy") {
				t.Errorf("argv = %q, want copied video with -copyts", args)
			}
		})
	}
}

func TestBuildHLSArgsPadsAfterTheDownmixBoost(t *testing.T) {
	job := lateAudioHLSJob("aac")
	job.AudioStream.Channels = 6
	boost := 2.0
	job.DownmixAudioBoost = &boost
	args := mustBuildHLSArgs(t, job)
	if got, want := args[argumentIndex(t, args, "-af")+1], "volume=2,aresample=osr=48000:async=1:first_pts=0"; got != want {
		t.Errorf("-af = %q, want %q", got, want)
	}
}

func TestBuildHLSArgsRejectsInvalidAudioPadding(t *testing.T) {
	for _, tc := range []struct {
		name string
		job  func() HLSJob
	}{
		{name: "pad with copy", job: func() HLSJob { return lateAudioHLSJob("copy") }},
		{name: "AC3 encode without pad", job: func() HLSJob {
			job := lateAudioHLSJob("ac3")
			job.PadAudioStart = false
			return job
		}},
		{name: "AC3 encode without bitrate", job: func() HLSJob {
			job := lateAudioHLSJob("ac3")
			job.OutputAudioBitrate = 0
			return job
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if args, err := BuildHLSArgs(tc.job(), progressiveTestCapabilities()); err == nil {
				t.Fatalf("BuildHLSArgs() = %q, want an error", args)
			}
		})
	}
}

func TestBuildHLSArgsDoesNotPadOnTimeAudio(t *testing.T) {
	job := lateAudioHLSJob("aac")
	job.PadAudioStart = false
	for _, arg := range mustBuildHLSArgs(t, job) {
		if strings.Contains(arg, "aresample") || arg == "-max_interleave_delta" {
			t.Fatalf("argv contains %q without PadAudioStart", arg)
		}
	}
}

// SPDX-License-Identifier: GPL-2.0-only

package encoding

import (
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestBuildHLSArgsMatchesGoldenCorpus(t *testing.T) {
	corpus := loadProgressiveGoldenCorpus(t)
	if got, want := len(corpus.Cases), 432; got != want {
		t.Fatalf("software transcode case count = %d, want %d", got, want)
	}

	for _, golden := range corpus.Cases {
		t.Run(golden.ID, func(t *testing.T) {
			assertProgressiveGoldenSourceFacts(t, golden)
			if golden.Argv.HLS == "" {
				t.Fatal("corpus row omits HLS argv")
			}
			want := splitFFmpegArgv(t, golden.Argv.HLS)
			got, err := BuildHLSArgs(hlsJobFromGolden(t, golden), progressiveCapabilitiesFromGolden(golden.Capabilities))
			if err != nil {
				t.Fatalf("BuildHLSArgs: %v", err)
			}
			if difference := argvDifference(want, got); difference != "" {
				t.Fatalf("HLS argv mismatch for %s:\n%s", golden.ID, difference)
			}
		})
	}
}

func TestBuildHLSArgsRejectsUnsafeOutputPaths(t *testing.T) {
	valid := hlsJobFromGolden(t, progressiveGoldenCase{
		Fixture:          "mp4-h264-ac3-aac-srt-2600k",
		InputCodec:       "h264",
		Subtitle:         "none",
		ToneMap:          "sdr",
		Audio:            "copy",
		Container:        "ts",
		Seek:             "start",
		SeekOffsetTicks:  0,
		NalLengthSize:    "0",
		AudioCodec:       "ac3",
		AudioStreamIndex: 1,
		AudioChannels:    6,
	})

	for _, test := range []struct {
		name string
		job  HLSJob
	}{
		{
			name: "empty HLS playlist and derived segment path",
			job: func() HLSJob {
				job := valid
				job.OutputPath = ""
				return job
			}(),
		},
		{
			name: "control character in HLS playlist and derived segment path",
			job: func() HLSJob {
				job := valid
				job.OutputPath = "/cache/goldenrec/master\x00-smuggled.m3u8"
				return job
			}(),
		},
		{
			name: "option-looking HLS playlist and derived segment path",
			job: func() HLSJob {
				job := valid
				job.OutputPath = "-hls_segment_filename"
				return job
			}(),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := BuildHLSArgs(test.job, progressiveTestCapabilities()); err == nil {
				t.Fatal("BuildHLSArgs accepted unsafe HLS output path")
			}
		})
	}
}

func TestBuildHLSArgsRejectsUnsupportedSegmentContainer(t *testing.T) {
	job := hlsJobFromGolden(t, progressiveGoldenCase{
		Fixture:          "mp4-h264-ac3-aac-srt-2600k",
		InputCodec:       "h264",
		Subtitle:         "none",
		ToneMap:          "sdr",
		Audio:            "copy",
		Container:        "ts",
		Seek:             "start",
		SeekOffsetTicks:  0,
		NalLengthSize:    "0",
		AudioCodec:       "ac3",
		AudioStreamIndex: 1,
		AudioChannels:    6,
	})
	job.OutputContainer = "mkv"

	if _, err := BuildHLSArgs(job, progressiveTestCapabilities()); err == nil {
		t.Fatal("BuildHLSArgs accepted unsupported HLS segment container")
	}
}

func hlsJobFromGolden(t *testing.T, golden progressiveGoldenCase) HLSJob {
	t.Helper()
	job := progressiveJobFromGolden(t, golden)
	job.OutputPath = "/cache/goldenrec/master.m3u8"
	job.CopyTimestamps = true
	job.VideoQuality.ForcedKeyFrameSeconds = 3

	switch golden.Container {
	case "ts", "fmp4":
		job.OutputContainer = golden.Container
	default:
		t.Fatalf("unknown HLS segment container %q", golden.Container)
	}
	return HLSJob{EncodingJob: job, SegmentLengthSeconds: 3, StartNumber: 0}
}

func TestBuildHLSArgsPropagatesSegmentControls(t *testing.T) {
	job := hlsUnitJob()
	job.SegmentLengthSeconds = 6
	job.StartNumber = 7

	args := mustBuildHLSArgs(t, job)
	if got := argumentValue(t, args, "-force_key_frames:0"); got != "expr:gte(t,n_forced*6)" {
		t.Fatalf("forced-keyframe expression = %q, want 6-second segment expression", got)
	}
	if got := argumentValue(t, args, "-hls_time"); got != "6" {
		t.Fatalf("HLS segment duration = %q, want 6", got)
	}
	if got := argumentValue(t, args, "-start_number"); got != "7" {
		t.Fatalf("HLS start number = %q, want 7", got)
	}
}

func TestBuildHLSArgsRejectsInvalidSegmentControls(t *testing.T) {
	for _, test := range []struct {
		name string
		job  HLSJob
	}{
		{name: "zero segment length", job: func() HLSJob { job := hlsUnitJob(); job.SegmentLengthSeconds = 0; return job }()},
		{name: "negative segment length", job: func() HLSJob { job := hlsUnitJob(); job.SegmentLengthSeconds = -1; return job }()},
		{name: "negative start number", job: func() HLSJob { job := hlsUnitJob(); job.StartNumber = -1; return job }()},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := BuildHLSArgs(test.job, progressiveTestCapabilities()); err == nil {
				t.Fatal("BuildHLSArgs accepted invalid HLS segment controls")
			}
		})
	}
}

func TestBuildHLSArgsAlwaysPreservesHLSInputTimestamps(t *testing.T) {
	job := hlsUnitJob()
	job.CopyTimestamps = false

	args := mustBuildHLSArgs(t, job)
	if !hasArgument(args, "-copyts") || !hasArgumentValue(args, "-avoid_negative_ts", "disabled") {
		t.Fatalf("HLS argv must preserve input timestamps: %q", args)
	}
}

func TestBuildHLSArgsPlacesFMP4SegmentOptionsAfterPlaylistFields(t *testing.T) {
	job := hlsUnitJob()
	job.OutputContainer = "fmp4"

	args := mustBuildHLSArgs(t, job)
	filename := argumentIndex(t, args, "-hls_segment_filename")
	playlistType := argumentIndex(t, args, "-hls_playlist_type")
	listSize := argumentIndex(t, args, "-hls_list_size")
	options := argumentIndex(t, args, "-hls_segment_options")
	output := argumentIndex(t, args, "-y")
	if filename >= playlistType || playlistType >= listSize || listSize >= options || options >= output {
		t.Fatalf("fMP4 option order is filename=%d playlist=%d list-size=%d options=%d output=%d; argv = %q", filename, playlistType, listSize, options, output, args)
	}
	if got := argumentValue(t, args, "-hls_segment_options"); got != "movflags=+frag_discont" {
		t.Fatalf("fMP4 segment options = %q, want movflags=+frag_discont", got)
	}
}

// GetInputModifier adds +genpts when the video is copied
// (EncodingHelper.cs:7382-7385); a transcode does not get it.
func TestBuildHLSArgsGeneratesInputTimestampsOnlyForVideoRemux(t *testing.T) {
	remux := hlsUnitJob()
	remux.OutputVideoCodec = "copy"
	remux.StartOffsetTicks = 900_000_000
	args := mustBuildHLSArgs(t, remux)
	if got := argumentValue(t, args, "-fflags"); got != "+genpts" {
		t.Fatalf("video-remux HLS -fflags = %q, want +genpts; argv = %q", got, args)
	}
	if seek, flags, input := argumentIndex(t, args, "-ss"), argumentIndex(t, args, "-fflags"), argumentIndex(t, args, "-i"); seek >= flags || flags >= input {
		t.Fatalf("remux input order is seek=%d fflags=%d input=%d, want seek, fflags, input; argv = %q", seek, flags, input, args)
	}

	if args := mustBuildHLSArgs(t, hlsUnitJob()); hasArgumentValue(args, "-fflags", "+genpts") {
		t.Fatalf("video-transcode HLS argv generated input timestamps: %q", args)
	}
}

// GetFastSeekCommandLineParameter (EncodingHelper.cs:3046-3072) seeks an HLS
// remux 0.5 s past the segment start, because an input seek to the exact
// keyframe time lands on the previous keyframe, and keeps every seek at least
// 5 s before the end of the media.
func TestBuildHLSArgsSeeksLikeUpstreamFastSeek(t *testing.T) {
	for _, tc := range []struct {
		name    string
		copy    bool
		start   int64
		runtime int64
		want    string
	}{
		{name: "remux seeks past the keyframe", copy: true, start: 130_130_000, runtime: 6_000_000_000, want: "00:00:13.513"},
		{name: "remux without runtime stays unclamped", copy: true, start: 130_130_000, want: "00:00:13.513"},
		{name: "transcode seeks to the segment start", start: 130_130_000, runtime: 6_000_000_000, want: "00:00:13.013"},
		{name: "remux clamps 5 s before the end", copy: true, start: 5_980_000_000, runtime: 6_000_000_000, want: "00:09:55.000"},
		{name: "transcode clamps 5 s before the end", start: 5_970_000_000, runtime: 6_000_000_000, want: "00:09:55.000"},
		{name: "short media clamps to zero", copy: true, start: 10_000_000, runtime: 30_000_000, want: "00:00:00.000"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			job := hlsUnitJob()
			if tc.copy {
				job.OutputVideoCodec = "copy"
			}
			job.StartOffsetTicks = tc.start
			job.RunTimeTicks = tc.runtime
			args := mustBuildHLSArgs(t, job)
			if got := argumentValue(t, args, "-ss"); got != tc.want {
				t.Fatalf("HLS seek = %q, want %q; argv = %q", got, tc.want, args)
			}
		})
	}
}

func TestBuildHLSArgsDoesNotOffsetAudioOnlySeek(t *testing.T) {
	job := hlsUnitJob()
	job.VideoStream = VideoStream{}
	job.OutputVideoCodec = ""
	job.OutputAudioCodec = "copy"
	job.StartOffsetTicks = 130_130_000
	job.RunTimeTicks = 6_000_000_000
	args := mustBuildHLSArgs(t, job)
	if got := argumentValue(t, args, "-ss"); got != "00:00:13.013" {
		t.Fatalf("audio-only HLS seek = %q, want 00:00:13.013; argv = %q", got, args)
	}
	if hasArgumentValue(args, "-fflags", "+genpts") {
		t.Fatalf("audio-only HLS argv generated input timestamps: %q", args)
	}
}

func TestBuildHLSArgsRejectsExternalPGSBurnIn(t *testing.T) {
	job := hlsUnitJob()
	job.SubtitleStream = &SubtitleStream{InputIndex: 2, Index: 7, Codec: "pgssub", IsExternal: true, BurnIn: true}

	if _, err := BuildHLSArgs(job, progressiveTestCapabilities()); err == nil {
		t.Fatal("BuildHLSArgs accepted external PGS burn-in without an HLS subtitle model")
	}
}

func TestBuildHLSArgsTreatsDVDSubtitlesAsGraphical(t *testing.T) {
	for _, codec := range []string{"dvdsub", "vobsub"} {
		t.Run(codec+" burn-in", func(t *testing.T) {
			job := hlsUnitJob()
			job.SubtitleStream = &SubtitleStream{InputIndex: 1, Index: 2, Codec: codec, Path: "/cache/graphical-subtitle.idx", IsExternal: true, BurnIn: true}
			if _, err := BuildHLSArgs(job, progressiveTestCapabilities()); err == nil {
				t.Fatalf("BuildHLSArgs accepted external %s burn-in as a libass subtitle", codec)
			}
		})
		t.Run(codec+" selected", func(t *testing.T) {
			job := hlsUnitJob()
			job.SubtitleStream = &SubtitleStream{InputIndex: 1, Index: 2, Codec: codec, IsExternal: true}
			args := mustBuildHLSArgs(t, job)
			if got := countArgument(args, "-start_at_zero"); got != 0 {
				t.Fatalf("selected external %s HLS argv has %d start-at-zero flags, want 0: %q", codec, got, args)
			}
		})
	}
}

func TestBuildHLSArgsRejectsGraphicalSubtitleFromSecondaryInput(t *testing.T) {
	job := hlsUnitJob()
	job.SubtitleStream = &SubtitleStream{InputIndex: 1, Index: 2, Codec: "pgssub", BurnIn: true}

	if _, err := BuildHLSArgs(job, progressiveTestCapabilities()); err == nil {
		t.Fatal("BuildHLSArgs accepted graphical subtitle from a secondary input without an input argument")
	}
}

func TestBuildHLSArgsPermitsInternalPGSBurnIn(t *testing.T) {
	job := hlsUnitJob()
	job.SubtitleStream = &SubtitleStream{InputIndex: 0, Index: 2, Codec: "pgssub", BurnIn: true}

	if _, err := BuildHLSArgs(job, progressiveTestCapabilities()); err != nil {
		t.Fatalf("BuildHLSArgs rejected internal PGS burn-in: %v", err)
	}
}

func TestBuildHLSArgsBurnsEmbeddedDVDSubtitles(t *testing.T) {
	for _, codec := range []string{"dvdsub", "vobsub"} {
		t.Run(codec, func(t *testing.T) {
			job := hlsUnitJob()
			job.SubtitleStream = &SubtitleStream{InputIndex: 0, Index: 3, Codec: codec, BurnIn: true}

			args, err := BuildHLSArgs(job, progressiveTestCapabilities())
			if err != nil {
				t.Fatalf("BuildHLSArgs rejected embedded %s burn-in: %v", codec, err)
			}
			if got := countArgument(args, "-filter_complex"); got != 1 {
				t.Fatalf("embedded %s HLS argv has %d -filter_complex graphs, want 1: %q", codec, got, args)
			}
			graph := argumentValue(t, args, "-filter_complex")
			streamLabel := "[0:3]"
			if !strings.HasPrefix(graph, streamLabel) {
				t.Fatalf("embedded %s overlay graph = %q, want it to read input 0 stream 3 first", codec, graph)
			}
			wantSuffix := "overlay=eof_action=pass:repeatlast=0"
			if !strings.HasSuffix(graph, wantSuffix) {
				t.Fatalf("embedded %s overlay graph = %q, want suffix %q", codec, graph, wantSuffix)
			}
			if strings.Contains(graph, "subtitles=") {
				t.Fatalf("embedded %s overlay graph = %q, must not use a subtitles= text filter", codec, graph)
			}
			for _, arg := range args {
				if strings.HasPrefix(arg, "subtitles=") {
					t.Fatalf("embedded %s HLS argv contains a subtitles= text filter: %q", codec, arg)
				}
			}
		})
	}
}

func TestBuildHLSArgsTrimsCopiedAudioAtSeekPoint(t *testing.T) {
	for _, test := range []struct {
		name  string
		ticks int64
		want  string
	}{
		{name: "whole seconds", ticks: 900_000_000, want: "noise=drop=lt(pts*tb\\,90.000)"},
		{name: "rounds to .NET F3 precision", ticks: 123_456_000, want: "noise=drop=lt(pts*tb\\,12.346)"},
		{name: "F3 source-double lower decimal midpoint", ticks: 123_445_000, want: "noise=drop=lt(pts*tb\\,12.345)"},
		{name: "F3 source-double higher decimal midpoint", ticks: 123_455_000, want: "noise=drop=lt(pts*tb\\,12.345)"},
		{name: "F3 tie rounds to even", ticks: 10_625_000, want: "noise=drop=lt(pts*tb\\,1.062)"},
	} {
		t.Run(test.name, func(t *testing.T) {
			job := hlsUnitJob()
			job.OutputAudioCodec = "copy"
			job.StartOffsetTicks = test.ticks

			args := mustBuildHLSArgs(t, job)
			if got := argumentValue(t, args, "-bsf:a"); got != test.want {
				t.Fatalf("copied-audio seek filter = %q, want %q", got, test.want)
			}
		})
	}
}

func TestBuildHLSArgsConvertsADTSForAACFMP4(t *testing.T) {
	for _, test := range []struct {
		name             string
		inputContainer   string
		outputAudioCodec string
	}{
		{name: "TS copied AAC", inputContainer: "ts", outputAudioCodec: "copy"},
		{name: "MPEG-TS transcoded AAC", inputContainer: "mpegts", outputAudioCodec: "aac"},
		{name: "AAC copied AAC", inputContainer: "aac", outputAudioCodec: "copy"},
		{name: "HLS transcoded AAC", inputContainer: "hls", outputAudioCodec: "aac"},
	} {
		t.Run(test.name, func(t *testing.T) {
			job := hlsUnitJob()
			job.InputContainer = test.inputContainer
			job.OutputContainer = "fmp4"
			job.AudioStream = AudioStream{Index: 1, Codec: "aac", Channels: 2}
			job.OutputAudioCodec = test.outputAudioCodec

			args := mustBuildHLSArgs(t, job)
			if got := argumentValue(t, args, "-bsf:a"); got != "aac_adtstoasc" {
				t.Fatalf("AAC fMP4 filter = %q, want aac_adtstoasc", got)
			}
		})
	}
}

func TestBuildHLSArgsOrdersTranscodedAudioArgumentsLikeUpstream(t *testing.T) {
	for _, test := range []struct {
		name       string
		sampleRate int
		ordered    [][2]string
	}{
		{
			name:       "with explicit sample rate",
			sampleRate: 48_000,
			ordered: [][2]string{
				{"-codec:a:0", "aac_at"},
				{"-bsf:a", "aac_adtstoasc"},
				{"-ac", "2"},
				{"-ab", "192000"},
				{"-ar", "48000"},
				{"-af", "volume=2"},
			},
		},
		{
			name: "without sample rate",
			ordered: [][2]string{
				{"-codec:a:0", "aac_at"},
				{"-bsf:a", "aac_adtstoasc"},
				{"-ac", "2"},
				{"-ab", "192000"},
				{"-af", "volume=2"},
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			job := hlsUnitJob()
			job.InputContainer = "ts"
			job.OutputContainer = "fmp4"
			job.AudioStream = AudioStream{Index: 1, Codec: "aac", Channels: 6}
			job.OutputAudioCodec = "aac"
			job.OutputAudioChannels = 2
			job.OutputAudioBitrate = 192_000
			job.OutputAudioSampleRate = test.sampleRate
			job.DownmixAudioBoost = float64Pointer(2)

			args := mustBuildHLSArgs(t, job)
			previous := -1
			for _, want := range test.ordered {
				index := argumentIndex(t, args, want[0])
				if index <= previous {
					t.Fatalf("transcoded audio flag %s at token %d follows token %d; argv = %q", want[0], index, previous, args)
				}
				if got := argumentValue(t, args, want[0]); got != want[1] {
					t.Fatalf("transcoded audio %s = %q, want %q", want[0], got, want[1])
				}
				previous = index
			}
		})
	}
}

func TestBuildHLSArgsKeepsAudioFilterWithoutSubtitleTimestampShift(t *testing.T) {
	job := hlsUnitJob()
	job.CopyTimestamps = true
	job.OutputAudioChannels = 2
	job.DownmixAudioBoost = float64Pointer(2)
	job.SubtitleStream = &SubtitleStream{InputIndex: 0, Index: 2, Codec: "srt", Path: "/cache/subtitles.srt", BurnIn: true}

	args := mustBuildHLSArgs(t, job)
	if got := argumentValue(t, args, "-af"); got != "volume=2" {
		t.Fatalf("HLS audio filter = %q, want only the applicable volume filter", got)
	}
}

func TestBuildHLSArgsOrdersCopiedAudioFiltersLikeUpstream(t *testing.T) {
	job := hlsUnitJob()
	job.InputContainer = "ts"
	job.OutputContainer = "fmp4"
	job.AudioStream = AudioStream{Index: 1, Codec: "aac", Channels: 2}
	job.OutputAudioCodec = "copy"
	job.StartOffsetTicks = 123_456_000

	args := mustBuildHLSArgs(t, job)
	if got := argumentValue(t, args, "-bsf:a"); got != "noise=drop=lt(pts*tb\\,12.346),aac_adtstoasc" {
		t.Fatalf("combined copied-audio filters = %q, want noise trim before aac_adtstoasc", got)
	}
}

func TestBuildHLSArgsSerializesLivePlaylistMode(t *testing.T) {
	for _, test := range []struct {
		name         string
		live         bool
		playlistType string
	}{
		{name: "VOD", playlistType: "vod"},
		{name: "live event", live: true, playlistType: "event"},
	} {
		t.Run(test.name, func(t *testing.T) {
			job := hlsUnitJob()
			job.Live = test.live
			job.StartNumber = 7

			args := mustBuildHLSArgs(t, job)
			if got := argumentValue(t, args, "-hls_playlist_type"); got != test.playlistType {
				t.Fatalf("HLS playlist type = %q, want %q", got, test.playlistType)
			}
			if !test.live {
				if hasArgument(args, "-hls_base_url") {
					t.Fatalf("VOD HLS argv unexpectedly has an event base URL: %q", args)
				}
				return
			}
			if got := argumentValue(t, args, "-hls_base_url"); got != "hls/master/" {
				t.Fatalf("event HLS base URL = %q, want hls/master/", got)
			}
			start := argumentIndex(t, args, "-start_number")
			baseURL := argumentIndex(t, args, "-hls_base_url")
			filename := argumentIndex(t, args, "-hls_segment_filename")
			playlist := argumentIndex(t, args, "-hls_playlist_type")
			if start >= baseURL || baseURL >= filename || filename >= playlist {
				t.Fatalf("event HLS option order is start=%d base-url=%d filename=%d playlist=%d; argv = %q", start, baseURL, filename, playlist, args)
			}
		})
	}
}

func TestBuildHLSArgsSerializesSeekWithStartNumber(t *testing.T) {
	job := hlsUnitJob()
	job.StartOffsetTicks = 900_000_000
	job.StartNumber = 7

	args := mustBuildHLSArgs(t, job)
	if got := argumentValue(t, args, "-ss"); got != "00:01:30.000" {
		t.Fatalf("HLS seek = %q, want 00:01:30.000", got)
	}
	if got := argumentValue(t, args, "-start_number"); got != "7" {
		t.Fatalf("HLS start number = %q, want 7", got)
	}
	if seek, input := argumentIndex(t, args, "-ss"), argumentIndex(t, args, "-i"); seek >= input {
		t.Fatalf("HLS seek token %d must precede input token %d; argv = %q", seek, input, args)
	}
}

func TestBuildHLSArgsUsesPublicLiveBaseURL(t *testing.T) {
	job := hlsUnitJob()
	job.OutputPath = "/cache/session-7.m3u8"
	job.Live = true

	args := mustBuildHLSArgs(t, job)
	if got := argumentValue(t, args, "-hls_base_url"); got != "hls/session-7/" {
		t.Fatalf("live HLS base URL = %q, want public hls/session-7/", got)
	}
}

func TestBuildHLSArgsSelectsFMP4OptionForEncoderVersion(t *testing.T) {
	for _, test := range []struct {
		name    string
		version int
		flag    string
	}{
		{name: "legacy FFmpeg", version: 4, flag: "-hls_ts_options"},
		{name: "modern FFmpeg", version: 5, flag: "-hls_segment_options"},
	} {
		t.Run(test.name, func(t *testing.T) {
			job := hlsUnitJob()
			job.OutputContainer = "fmp4"
			caps := progressiveTestCapabilities()
			caps.EncoderMajorVersion = test.version

			args := mustBuildHLSArgsWithCaps(t, job, caps)
			if got := argumentValue(t, args, test.flag); got != "movflags=+frag_discont" {
				t.Fatalf("fMP4 %s = %q, want movflags=+frag_discont", test.flag, got)
			}
		})
	}
}

func TestBuildHLSArgsAddsStartAtZeroForSelectedSubtitle(t *testing.T) {
	job := hlsUnitJob()
	job.SubtitleStream = &SubtitleStream{InputIndex: 0, Index: 2, Codec: "srt"}

	args := mustBuildHLSArgs(t, job)
	if got := countArgument(args, "-start_at_zero"); got != 1 {
		t.Fatalf("selected-subtitle HLS argv has %d top-level start-at-zero flags, want 1: %q", got, args)
	}
}

func TestBuildHLSArgsUsesWindowsSafeLiveBaseURL(t *testing.T) {
	job := hlsUnitJob()
	job.OutputPath = `C:\cache\session-7.m3u8`
	job.Live = true

	args := mustBuildHLSArgs(t, job)
	if got := argumentValue(t, args, "-hls_base_url"); got != "hls/session-7/" {
		t.Fatalf("Windows-path live HLS base URL = %q, want public hls/session-7/", got)
	}
}

func TestBuildHLSArgsTagsCompatibleDirectDolbyVisionHEVCAsHVC1(t *testing.T) {
	for _, profile := range []int{8, 10} {
		t.Run("profile "+strconv.Itoa(profile), func(t *testing.T) {
			job := hlsUnitJob()
			job.OutputContainer = "fmp4"
			job.OutputVideoCodec = "copy"
			job.DirectDolbyVision = true
			job.VideoStream = VideoStream{
				Index:                        0,
				Codec:                        "hevc",
				NalLengthSize:                "0",
				DolbyVisionProfile:           profile,
				RPUFlag:                      1,
				BLPresentFlag:                1,
				DolbyVisionBLCompatibilityID: 4,
			}

			args := mustBuildHLSArgs(t, job)
			if got := argumentValue(t, args, "-tag:v:0"); got != "hvc1" {
				t.Fatalf("DOVI profile %d HEVC tag = %q, want hvc1", profile, got)
			}
			if got := argumentValue(t, args, "-strict"); got != "-2" {
				t.Fatalf("DOVI profile %d strictness = %q, want -2", profile, got)
			}
		})
	}
}

func TestBuildHLSArgsRequiresDirectDolbyVisionOnlyWithoutBaseCompatibility(t *testing.T) {
	for _, test := range []struct {
		name          string
		profile       int
		compatibility int
		wantError     bool
	}{
		{name: "profile 5", profile: 5, compatibility: 0, wantError: true},
		{name: "profile 8 without compatibility", profile: 8, compatibility: 0, wantError: true},
		{name: "HDR10 fallback", profile: 8, compatibility: 1},
		{name: "SDR fallback", profile: 8, compatibility: 2},
		{name: "HLG fallback", profile: 8, compatibility: 4},
	} {
		t.Run(test.name, func(t *testing.T) {
			job := hlsUnitJob()
			job.OutputVideoCodec = "copy"
			job.VideoStream = VideoStream{
				Index:                        0,
				Codec:                        "hevc",
				NalLengthSize:                "0",
				DolbyVisionProfile:           test.profile,
				RPUFlag:                      1,
				BLPresentFlag:                1,
				DolbyVisionBLCompatibilityID: test.compatibility,
			}

			_, err := BuildHLSArgs(job, progressiveTestCapabilities())
			if (err != nil) != test.wantError {
				t.Fatalf("DOVI profile %d compatibility %d error = %v, want error=%t", test.profile, test.compatibility, err, test.wantError)
			}
		})
	}
}

func TestBuildHLSArgsEnablesFMP4ExperimentalCopiedAudio(t *testing.T) {
	for _, test := range []struct {
		name    string
		codec   string
		version int
		strict  bool
	}{
		{name: "opus on FFmpeg 5", codec: "opus", version: 5, strict: true},
		{name: "DTS on FFmpeg 5", codec: "dts", version: 5, strict: true},
		{name: "TrueHD on FFmpeg 5", codec: "truehd", version: 5, strict: true},
		{name: "FLAC on FFmpeg 5", codec: "flac", version: 5, strict: true},
		{name: "opus on FFmpeg 7", codec: "opus", version: 7, strict: true},
		{name: "TrueHD on FFmpeg 7", codec: "truehd", version: 7, strict: true},
		{name: "FLAC on FFmpeg 6", codec: "flac", version: 6},
		{name: "AAC on FFmpeg 5", codec: "aac", version: 5},
	} {
		t.Run(test.name, func(t *testing.T) {
			job := hlsUnitJob()
			job.OutputContainer = "fmp4"
			job.AudioStream = AudioStream{Index: 1, Codec: test.codec, Channels: 2}
			job.OutputAudioCodec = "copy"
			caps := progressiveTestCapabilities()
			caps.EncoderMajorVersion = test.version

			args := mustBuildHLSArgsWithCaps(t, job, caps)
			got := hasArgumentValue(args, "-strict", "-2")
			if got != test.strict {
				t.Fatalf("copied %s strictness on FFmpeg %d = %t, want %t; argv = %q", test.codec, test.version, got, test.strict, args)
			}
			if test.strict {
				if copyIndex, strictIndex := argumentIndex(t, args, "-codec:a:0"), argumentIndex(t, args, "-strict"); copyIndex >= strictIndex {
					t.Fatalf("copied %s strictness token %d must follow codec token %d; argv = %q", test.codec, strictIndex, copyIndex, args)
				}
			}
		})
	}
}

func hlsUnitJob() HLSJob {
	job := progressiveUnitJob()
	job.OutputPath = "/cache/hls/master.m3u8"
	job.OutputContainer = "ts"
	job.CopyTimestamps = true
	job.VideoQuality.ForcedKeyFrameSeconds = 3
	return HLSJob{EncodingJob: job, SegmentLengthSeconds: 3, StartNumber: 0}
}

func TestBuildHLSArgsUsesPlatformFMP4InitFilename(t *testing.T) {
	outputPath, want := "/cache/session.m3u8", "session-1.mp4"
	if runtime.GOOS == "windows" {
		outputPath, want = `C:\cache\session.m3u8`, `C:\cache\session-1.mp4`
	}
	job := hlsUnitJob()
	job.OutputPath = outputPath
	job.OutputContainer = "fmp4"

	args := mustBuildHLSArgs(t, job)
	if got := argumentValue(t, args, "-hls_fmp4_init_filename"); got != want {
		t.Fatalf("fMP4 init filename = %q, want %q for %s", got, want, runtime.GOOS)
	}
}

func TestBuildHLSArgsUsesGlobalHeaderOnlyForLiveTS(t *testing.T) {
	for _, test := range []struct {
		name      string
		live      bool
		container string
		want      bool
	}{
		{name: "VOD TS", container: "ts"},
		{name: "live fMP4", live: true, container: "fmp4"},
		{name: "live TS", live: true, container: "ts", want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			job := hlsUnitJob()
			job.Live = test.live
			job.OutputContainer = test.container
			if test.want {
				job.SubtitleStream = &SubtitleStream{InputIndex: 0, Index: 2, Codec: "srt"}
			}

			args := mustBuildHLSArgs(t, job)
			got := hasArgumentValue(args, "-flags", "-global_header")
			if got != test.want {
				t.Fatalf("live=%t container=%s global-header flag = %t, want %t; argv = %q", test.live, test.container, got, test.want, args)
			}
			if !test.want {
				return
			}
			videoEnd := argumentIndex(t, args, "-sc_threshold:v:0")
			startAtZero := argumentIndex(t, args, "-start_at_zero")
			flags := argumentIndex(t, args, "-flags")
			audio := argumentIndex(t, args, "-codec:a:0")
			if videoEnd >= startAtZero || startAtZero >= flags || flags >= audio {
				t.Fatalf("live TS global-header order is video=%d start=%d flags=%d audio=%d; argv = %q", videoEnd, startAtZero, flags, audio, args)
			}
		})
	}
}

func TestBuildHLSArgsOmitsStartAtZeroForSelectedExternalGraphicalSubtitle(t *testing.T) {
	job := hlsUnitJob()
	job.SubtitleStream = &SubtitleStream{InputIndex: 1, Index: 2, Codec: "pgssub", IsExternal: true}

	args := mustBuildHLSArgs(t, job)
	if got := countArgument(args, "-start_at_zero"); got != 0 {
		t.Fatalf("external graphical subtitle HLS argv has %d top-level start-at-zero flags, want 0: %q", got, args)
	}
}

func TestBuildHLSArgsAlwaysStartsCopiedVideoAtZero(t *testing.T) {
	job := hlsUnitJob()
	job.OutputVideoCodec = "copy"

	args := mustBuildHLSArgs(t, job)
	if got := countArgument(args, "-start_at_zero"); got != 1 {
		t.Fatalf("video-copy HLS argv has %d top-level start-at-zero flags, want 1: %q", got, args)
	}
}

func mustBuildHLSArgsWithCaps(t *testing.T, job HLSJob, caps Capabilities) []string {
	t.Helper()
	args, err := BuildHLSArgs(job, caps)
	if err != nil {
		t.Fatalf("BuildHLSArgs: %v", err)
	}
	return args
}

func countArgument(args []string, want string) int {
	count := 0
	for _, argument := range args {
		if argument == want {
			count++
		}
	}
	return count
}

func mustBuildHLSArgs(t *testing.T, job HLSJob) []string {
	t.Helper()
	args, err := BuildHLSArgs(job, progressiveTestCapabilities())
	if err != nil {
		t.Fatalf("BuildHLSArgs: %v", err)
	}
	return args
}

func argumentIndex(t *testing.T, args []string, flag string) int {
	t.Helper()
	for index, argument := range args {
		if argument == flag {
			return index
		}
	}
	t.Fatalf("argv %q lacks %s", args, flag)
	return -1
}

func TestBuildHLSArgsBurnsEmbeddedSelectedTextSubtitleFromMediaInput(t *testing.T) {
	job := hlsUnitJob()
	job.InputPath = "/media/feature-with-two-subtitles.mkv"
	job.SubtitleStream = &SubtitleStream{InputIndex: 0, Index: 5, SubtitleIndex: 1, Codec: "srt", BurnIn: true}

	args := mustBuildHLSArgs(t, job)
	filter := argumentValue(t, args, "-vf")
	if !strings.Contains(filter, "subtitles=f='/media/feature-with-two-subtitles.mkv':si=1") {
		t.Fatalf("embedded selected subtitle filter = %q, want media input and second subtitle stream", filter)
	}
	if strings.Contains(filter, "subtitles=f=''") {
		t.Fatalf("embedded selected subtitle filter used an empty external path: %q", filter)
	}
}

func TestHLSEmbeddedSubtitleFilterSourceMatchesSingleHostilePathOperand(t *testing.T) {
	job := hlsUnitJob()
	job.InputPath = "/media/it's: weird,dir/input.mkv"
	job.SubtitleStream = &SubtitleStream{InputIndex: 0, Index: 5, SubtitleIndex: 1, Codec: "srt", BurnIn: true}

	token := HLSEmbeddedSubtitleFilterSource(job)
	if want := `subtitles=f='/media/it'\\\''s\: weird,dir/input.mkv'`; token != want {
		t.Fatalf("HLSEmbeddedSubtitleFilterSource() = %q, want %q", token, want)
	}

	args := mustBuildHLSArgs(t, job)
	filter := argumentValue(t, args, "-vf")
	if got := strings.Count(filter, token); got != 1 {
		t.Fatalf("embedded subtitle token appears %d times in -vf %q, want once", got, filter)
	}
	if !strings.Contains(filter, token+":si=1") {
		t.Fatalf("embedded subtitle filter = %q, want exact token followed by selected subtitle index", filter)
	}
}

func TestHLSEmbeddedSubtitleFilterSourceIsEmptyWithoutEmbeddedTextBurn(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*HLSJob)
	}{
		{
			name: "external burn",
			mutate: func(job *HLSJob) {
				job.SubtitleStream = &SubtitleStream{InputIndex: 1, Index: 2, Codec: "srt", Path: "/cache/external.srt", BurnIn: true, IsExternal: true}
			},
		},
		{
			name: "PGS burn",
			mutate: func(job *HLSJob) {
				job.SubtitleStream = &SubtitleStream{InputIndex: 0, Index: 2, Codec: "pgssub", BurnIn: true}
			},
		},
		{
			name: "video copy",
			mutate: func(job *HLSJob) {
				job.OutputVideoCodec = "copy"
				job.SubtitleStream = &SubtitleStream{InputIndex: 0, Index: 2, Codec: "srt", BurnIn: true}
			},
		},
		{
			name: "audio only",
			mutate: func(job *HLSJob) {
				job.VideoStream = VideoStream{}
				job.OutputVideoCodec = ""
				job.SubtitleStream = &SubtitleStream{InputIndex: 0, Index: 2, Codec: "srt", BurnIn: true}
			},
		},
		{
			name: "subtitle does not burn",
			mutate: func(job *HLSJob) {
				job.SubtitleStream = &SubtitleStream{InputIndex: 0, Index: 2, Codec: "srt"}
			},
		},
		{
			name: "no subtitle",
			mutate: func(job *HLSJob) {
				job.SubtitleStream = nil
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			job := hlsUnitJob()
			test.mutate(&job)
			if got := HLSEmbeddedSubtitleFilterSource(job); got != "" {
				t.Fatalf("HLSEmbeddedSubtitleFilterSource() = %q, want empty", got)
			}
		})
	}
}

func TestHLSSubtitleFilterSourceReturnsExternalTextBurnOperand(t *testing.T) {
	job := hlsUnitJob()
	job.SubtitleStream = &SubtitleStream{
		InputIndex: 1, Index: 2, Codec: "srt", Path: `/cache/it’s: feature,dir/subtitle.srt`,
		BurnIn: true, IsExternal: true,
	}
	token := HLSSubtitleFilterSource(job)
	args := mustBuildHLSArgs(t, job)
	filter := argumentValue(t, args, "-vf")
	if token == "" || strings.Count(filter, token) != 1 {
		t.Fatalf("external subtitle token/filter = %q/%q, want one exact operand", token, filter)
	}
	if got := HLSEmbeddedSubtitleFilterSource(job); got != "" {
		t.Fatalf("embedded subtitle token = %q, want empty for external burn", got)
	}
}

func TestBuildHLSArgsBurnsExtractedTextSubtitleWithFontsDir(t *testing.T) {
	job := hlsUnitJob()
	job.InputPath = "/media/show.mkv"
	job.SubtitleStream = &SubtitleStream{
		Index: 2, SubtitleIndex: 0, Codec: "ass", BurnIn: true,
		ExtractedPath: "/data/subtitles/ab/id/2.ass",
		FontsDir:      "/data/attachments/ab/it's: fonts",
	}

	token := HLSSubtitleFilterSource(job)
	if want := "subtitles=f='/data/subtitles/ab/id/2.ass'"; token != want {
		t.Fatalf("HLSSubtitleFilterSource() = %q, want %q", token, want)
	}
	if got := HLSEmbeddedSubtitleFilterSource(job); got != "" {
		t.Fatalf("HLSEmbeddedSubtitleFilterSource() = %q, want empty: the media file is not a subtitle source", got)
	}
	filter := argumentValue(t, mustBuildHLSArgs(t, job), "-vf")
	if want := token + `:fontsdir='/data/attachments/ab/it'\\\''s\: fonts'`; !strings.Contains(filter, want) {
		t.Fatalf("extracted subtitle filter = %q, want %q", filter, want)
	}
	if strings.Contains(filter, "/media/show.mkv") || strings.Contains(filter, ":si=") {
		t.Fatalf("extracted subtitle filter = %q, want no media-file subtitle read", filter)
	}
}

func TestAllAttachmentsDumpArgsDumpsEveryAttachmentInOnePass(t *testing.T) {
	dumps := []AttachmentDump{{Index: 3, OutputPath: "/a/One.ttf.part"}, {Index: 5, OutputPath: "/a/Two.otf.part"}}
	got := AllAttachmentsDumpArgs("/dev/fd/3", dumps, true)
	want := []string{"-nostdin", "-dump_attachment:3", "/a/One.ttf.part", "-dump_attachment:5", "/a/Two.otf.part", "-i", "/dev/fd/3", "-t", "0", "-f", "null", "null"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("AllAttachmentsDumpArgs() = %q, want %q", got, want)
	}
	if got := AllAttachmentsDumpArgs("/dev/fd/3", dumps[:1], false); strings.Join(got, " ") != "-nostdin -dump_attachment:3 /a/One.ttf.part -i /dev/fd/3" {
		t.Fatalf("AllAttachmentsDumpArgs(no A/V) = %q", got)
	}
}

func TestBuildHLSArgsHonorsConstrainedH264ProfileAndLevel(t *testing.T) {
	for _, test := range []struct {
		name, profile, wantProfile, wantLevel string
		level                                 int
	}{
		{name: "High at 4.1", profile: "High", wantProfile: "high", level: 41, wantLevel: "4.1"},
		{name: "Main at 4.0", profile: "Main", wantProfile: "main", level: 40, wantLevel: "4.0"},
		{name: "Baseline at 4.0", profile: "Baseline", wantProfile: "baseline", level: 40, wantLevel: "4.0"},
	} {
		t.Run(test.name, func(t *testing.T) {
			job := hlsUnitJob()
			job.OutputVideoProfile = test.profile
			job.OutputVideoLevel = test.level

			args := mustBuildHLSArgs(t, job)
			if got := argumentValue(t, args, "-profile:v:0"); got != test.wantProfile {
				t.Fatalf("H264 profile argv = %q, want exact constrained profile %q; argv=%q", got, test.wantProfile, args)
			}
			if got := argumentValue(t, args, "-level:v:0"); got != test.wantLevel {
				t.Fatalf("H264 level argv = %q, want exact constrained level %q; argv=%q", got, test.wantLevel, args)
			}
		})
	}
}

// A keyframe-cut remux cuts at every source keyframe and seeks exactly where
// the caller says; the 0.5 s offset and the end clamp do not apply
// (deadfin#629).
func TestBuildHLSArgsKeyframeCutsSegmentEveryKeyframe(t *testing.T) {
	job := hlsUnitJob()
	job.OutputVideoCodec = "copy"
	job.KeyframeCuts = true
	job.StartOffsetTicks = 5_981_000_000
	job.RunTimeTicks = 6_000_000_000
	job.StartNumber = 42

	args := mustBuildHLSArgs(t, job)
	if got := argumentValue(t, args, "-hls_time"); got != "0" {
		t.Fatalf("keyframe-cut -hls_time = %q, want 0; argv = %q", got, args)
	}
	if got := argumentValue(t, args, "-ss"); got != "00:09:58.100" {
		t.Fatalf("keyframe-cut seek = %q, want the exact 00:09:58.100; argv = %q", got, args)
	}
	if got := argumentValue(t, args, "-start_number"); got != "42" {
		t.Fatalf("keyframe-cut start number = %q, want 42", got)
	}
}

func TestBuildHLSArgsRejectsKeyframeCutsOutsideVODVideoCopy(t *testing.T) {
	transcode := hlsUnitJob()
	transcode.KeyframeCuts = true
	live := hlsUnitJob()
	live.OutputVideoCodec = "copy"
	live.Live = true
	live.KeyframeCuts = true
	for name, job := range map[string]HLSJob{"transcode": transcode, "live": live} {
		if _, err := BuildHLSArgs(job, Capabilities{H264Encoder: "libx264", AACEncoders: []string{"aac"}}); err == nil {
			t.Errorf("%s keyframe-cut job built, want an error", name)
		}
	}
}

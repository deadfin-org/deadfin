// SPDX-License-Identifier: GPL-2.0-only

package playback_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/deadfin-org/deadfin/internal/playback"
)

func TestHLSServiceTranscodeProjectsOutputFramerateToArgvAndMaster(t *testing.T) {
	for _, test := range []struct {
		name       string
		sourceRate float32
		maxRate    *float32
		explicit   *float32
		wantRate   string
	}{
		{name: "max rate caps sixty fps source", sourceRate: 60, maxRate: hlsFloat32(30), wantRate: "30"},
		{name: "larger max rate preserves sixty fps source", sourceRate: 60, maxRate: hlsFloat32(120), wantRate: "60"},
		{name: "omitted max rate preserves sixty fps source", sourceRate: 60, wantRate: "60"},
		{name: "explicit rate overrides lower source rate", sourceRate: 23.976, explicit: hlsFloat32(24), wantRate: "24"},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := progressiveCopySource("/media/frame-rate-feature.mkv")
			source.MediaStreams[0].AverageFrameRate = &test.sourceRate
			manager := &hlsFakeManager{playlist: "#EXTM3U\n"}
			service := newHLSService(&progressiveFakeSource{source: source}, manager)
			request := hlsTranscodeRequest()
			request.MaxFramerate = test.maxRate
			request.Framerate = test.explicit

			response, err := service.Master(t.Context(), request)
			if err != nil {
				t.Fatalf("Master(): %v", err)
			}
			playlist := readHLSResponse(t, response)
			if !strings.Contains(playlist, "FRAME-RATE="+test.wantRate) {
				t.Fatalf("Master() playlist = %q, want target FRAME-RATE=%s", playlist, test.wantRate)
			}

			if _, err := service.Live(t.Context(), request); err != nil {
				t.Fatalf("Live(): %v", err)
			}
			gotRate := hlsConstraintArgumentValue(manager.lastStart.Args, "-r")
			wantArgRate := test.wantRate
			if test.maxRate == nil && test.explicit == nil || test.maxRate != nil && *test.maxRate >= test.sourceRate {
				wantArgRate = ""
			}
			if gotRate != wantArgRate {
				t.Fatalf("Live() output rate = %q, want %q; argv = %q", gotRate, wantArgRate, manager.lastStart.Args)
			}
		})
	}
}

func TestHLSServiceMasterUsesDrawingUtilsResizeProjection(t *testing.T) {
	for _, test := range []struct {
		name                string
		width, height       int
		maxWidth, maxHeight *int
		wantResolution      string
	}{
		{name: "maximum width", maxWidth: hlsInt(1280), wantResolution: "1280x720"},
		{name: "maximum height", maxHeight: hlsInt(720), wantResolution: "1280x720"},
		{name: "fixed width rounds aspect height", width: 1281, wantResolution: "1281x721"},
		{name: "fixed height rounds aspect width", height: 721, wantResolution: "1282x721"},
		{name: "fixed dimensions override source aspect", width: 1281, height: 721, wantResolution: "1281x721"},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := progressiveCopySource("/media/1920x1080-feature.mkv")
			width, height := 1920, 1080
			source.MediaStreams[0].Width, source.MediaStreams[0].Height = &width, &height
			// The source bitrate caps the output bitrate. Keep both high so
			// the resolution normalizer does not cap the width.
			source.MediaStreams[0].BitRate = hlsInt(50_000_000)
			service := newHLSService(&progressiveFakeSource{source: source}, &hlsFakeManager{})
			request := hlsTranscodeRequest()
			request.VideoBitrate = hlsInt(50_000_000)
			request.Width, request.Height = hlsInt(test.width), hlsInt(test.height)
			request.MaxWidth, request.MaxHeight = test.maxWidth, test.maxHeight

			response, err := service.Master(t.Context(), request)
			if err != nil {
				t.Fatalf("Master(): %v", err)
			}
			playlist := readHLSResponse(t, response)
			if !strings.Contains(playlist, "RESOLUTION="+test.wantResolution) {
				t.Fatalf("Master() playlist = %q, want DrawingUtils.Resize resolution %s", playlist, test.wantResolution)
			}
		})
	}
}

func TestHLSServiceDoesNotHardcodeUnrequestedTranscodeCodecs(t *testing.T) {
	for _, test := range []struct {
		name      string
		configure func(*playback.HLSRequest)
		wantError bool
	}{
		{
			name: "AV1-only video request rejects unsupported encoder",
			configure: func(request *playback.HLSRequest) {
				request.VideoCodec, request.VideoCodecs = "av1", []string{"av1"}
			},
			wantError: true,
		},
		{
			name: "H264 video request remains supported",
			configure: func(request *playback.HLSRequest) {
				request.VideoCodec, request.VideoCodecs = "h264", []string{"h264"}
			},
		},
		{
			name: "MP3-only audio request rejects unsupported encoder",
			configure: func(request *playback.HLSRequest) {
				request.AudioCodec, request.AudioCodecs = "mp3", []string{"mp3"}
			},
			wantError: true,
		},
		{
			name: "AAC audio request remains supported",
			configure: func(request *playback.HLSRequest) {
				request.AudioCodec, request.AudioCodecs = "aac", []string{"aac"}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := newHLSService(&progressiveFakeSource{source: progressiveCopySource("/media/codec-feature.mkv")}, &hlsFakeManager{})
			request := hlsTranscodeRequest()
			request.AllowVideoStreamCopy = false
			request.AllowAudioStreamCopy = false
			request.EnableAutoStreamCopy = false
			test.configure(&request)

			response, err := service.Master(t.Context(), request)
			if test.wantError {
				if !errors.Is(err, playback.ErrProgressiveUnsupported) {
					t.Fatalf("Master() error = %v, want ErrProgressiveUnsupported", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Master(): %v", err)
			}
			playlist := readHLSResponse(t, response)
			if !strings.Contains(playlist, "CODECS=\"avc1.640029,mp4a.40.2\"") {
				t.Fatalf("Master() supported-codec playlist = %q, want H264/AAC rendition", playlist)
			}
		})
	}
}

// Copied MediaStream.Level values arrive from the media source already in the
// normalized integer representation RFC 6381 expects. Multiplying them as if
// they were decimal display levels turns e.g. HEVC 153 into L1530.
func TestHLSServiceMasterPreservesNormalizedCopiedVideoLevels(t *testing.T) {
	for _, test := range []struct {
		name, codec, profile, want string
		level                      float64
		bitDepth                   int
	}{
		{name: "H264 level 41", codec: "h264", profile: "High", level: 41, bitDepth: 8, want: "avc1.640029"},
		{name: "HEVC level 153", codec: "hevc", profile: "Main", level: 153, bitDepth: 8, want: "hvc1.1.4.L153.B0"},
		{name: "AV1 level 13", codec: "av1", profile: "Main", level: 13, bitDepth: 8, want: "av01.0.13M.08"},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := progressiveCopySource("/media/normalized-level-" + test.codec + ".mkv")
			video := &source.MediaStreams[0]
			video.Codec = test.codec
			video.Profile = &test.profile
			video.Level = &test.level
			video.BitDepth = &test.bitDepth

			request := hlsMasterCopyRequest(test.codec)
			response, err := newHLSService(&progressiveFakeSource{source: source}, &hlsFakeManager{}).Master(t.Context(), request)
			if err != nil {
				t.Fatalf("Master(): %v", err)
			}
			playlist := readHLSResponse(t, response)
			if !strings.Contains(playlist, "CODECS=\""+test.want+",") {
				t.Fatalf("Master() copied %s level %v playlist = %q, want exact RFC 6381 token %q", test.codec, test.level, playlist, test.want)
			}
		})
	}
}

func TestHLSServiceRotatedSourceUsesAutorotationAndSwappedSizing(t *testing.T) {
	for _, test := range []struct {
		name           string
		rotation       int
		wantResolution string
	}{
		{name: "ninety degrees swaps source dimensions", rotation: 90, wantResolution: "405x720"},
		{name: "negative ninety degrees swaps source dimensions", rotation: -90, wantResolution: "405x720"},
		{name: "one hundred eighty degrees preserves source dimensions", rotation: 180, wantResolution: "1280x720"},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := progressiveCopySource("/media/rotated-1920x1080-feature.mkv")
			width, height := 1920, 1080
			source.MediaStreams[0].Width, source.MediaStreams[0].Height = &width, &height
			source.MediaStreams[0].Rotation = &test.rotation
			manager := &hlsFakeManager{playlist: "#EXTM3U\n"}
			service := newHLSService(&progressiveFakeSource{source: source}, manager)
			request := hlsTranscodeRequest()
			request.VideoBitrate = hlsInt(50_000_000)
			request.Width, request.Height = nil, nil
			request.MaxWidth, request.MaxHeight = hlsInt(1280), hlsInt(720)

			response, err := service.Master(t.Context(), request)
			if err != nil {
				t.Fatalf("Master(): %v", err)
			}
			playlist := readHLSResponse(t, response)
			if !strings.Contains(playlist, "RESOLUTION="+test.wantResolution) {
				t.Fatalf("Master() rotated playlist = %q, want resolution %s", playlist, test.wantResolution)
			}

			if _, err := service.Live(t.Context(), request); err != nil {
				t.Fatalf("Live(): %v", err)
			}
			args := manager.lastStart.Args
			if strings.Contains(hlsConstraintArgumentValue(args, "-vf"), "transpose=") || hlsHasArg(args, "-noautorotate") {
				t.Fatalf("Live() rotated source must rely on ffmpeg autorotation, not manual transpose: %q", args)
			}
		})
	}
}

func TestHLSServiceMasterSignalsHLGForCopiedHLGBaseLayers(t *testing.T) {
	for _, test := range []struct {
		name             string
		codec            string
		requestedRange   string
		configure        func(*playback.MediaStream)
		wantSupplemental string
		wantSDRCompanion bool
	}{
		{
			name:  "HLG",
			codec: "h264",
			configure: func(video *playback.MediaStream) {
				transfer := "arib-std-b67"
				video.ColorTransfer = &transfer
			},
			wantSDRCompanion: true,
		},
		{
			name:           "Dolby Vision with HLG base layer",
			codec:          "hevc",
			requestedRange: "HLG",
			configure: func(video *playback.MediaStream) {
				profile, level, rpu, base, compatibility := 8, 6, 1, 1, 4
				video.DvProfile, video.DvLevel = &profile, &level
				video.RpuPresentFlag, video.BlPresentFlag, video.DvBlSignalCompatibilityID = &rpu, &base, &compatibility
			},
			wantSDRCompanion: true,
			wantSupplemental: "SUPPLEMENTAL-CODECS=\"dvh1.08.06/db4h\"",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := progressiveCopySource("/media/hlg-copy-feature.mkv")
			video := &source.MediaStreams[0]
			video.Codec = test.codec
			if test.codec == "hevc" {
				profile, bitDepth := "Main 10", 10
				video.Profile, video.BitDepth = &profile, &bitDepth
			}
			test.configure(video)
			service := newHLSService(&progressiveFakeSource{source: source}, &hlsFakeManager{})
			request := hlsMasterCopyRequest(test.codec)
			if test.requestedRange != "" {
				request.StreamOptions = playback.NewStreamOptions()
				request.StreamOptions.Set(test.codec+"-rangetype", test.requestedRange)
			}
			response, err := service.Master(t.Context(), request)
			if err != nil {
				t.Fatalf("Master(): %v", err)
			}
			playlist := readHLSResponse(t, response)
			if !strings.Contains(playlist, "VIDEO-RANGE=HLG") || strings.Contains(playlist, "VIDEO-RANGE=PQ") {
				t.Fatalf("Master() %s playlist = %q, want HLG rather than PQ", test.name, playlist)
			}
			if test.wantSupplemental != "" && !strings.Contains(playlist, test.wantSupplemental) {
				t.Fatalf("Master() %s playlist = %q, want %s", test.name, playlist, test.wantSupplemental)
			}
			gotSDRCompanion := strings.Contains(playlist, "main.m3u8?VideoCodec=h264&AllowVideoStreamCopy=false")
			if gotSDRCompanion != test.wantSDRCompanion {
				t.Fatalf("Master() %s SDR companion = %t, want %t; playlist = %q", test.name, gotSDRCompanion, test.wantSDRCompanion, playlist)
			}
		})
	}
}

func TestHLSServiceMasterGatesDirectDolbyAndSDRCompanionVariants(t *testing.T) {
	makeDOVISource := func() playback.MediaSourceInfo {
		source := progressiveCopySource("/media/dovi-copy-feature.mkv")
		video := &source.MediaStreams[0]
		video.Codec = "hevc"
		profileName, bitDepth := "Main 10", 10
		profile, level, rpu, base, compatibility := 5, 9, 1, 1, 0
		video.Profile, video.BitDepth = &profileName, &bitDepth
		video.DvProfile, video.DvLevel = &profile, &level
		video.RpuPresentFlag, video.BlPresentFlag, video.DvBlSignalCompatibilityID = &rpu, &base, &compatibility
		return source
	}

	for _, test := range []struct {
		name             string
		source           playback.MediaSourceInfo
		request          playback.HLSRequest
		wantError        bool
		wantDirectDOVI   bool
		wantSDRCompanion bool
		wantVariantCount int
		wantVideoRange   string
	}{
		{
			name:             "explicit Dolby Vision client gets direct and SDR variants",
			source:           makeDOVISource(),
			request:          hlsMasterDOVIRequest(),
			wantDirectDOVI:   true,
			wantSDRCompanion: true,
			wantVariantCount: 3,
			wantVideoRange:   "VIDEO-RANGE=PQ",
		},
		{
			name:      "Dolby fallback-only request rejects copy",
			source:    makeDOVISource(),
			request:   hlsMasterCopyRequest("hevc"),
			wantError: true,
		},
		{
			name: "HDR10 copy gets H264 SDR companion",
			source: func() playback.MediaSourceInfo {
				source := progressiveCopySource("/media/hdr10-copy-feature.mkv")
				transfer := "smpte2084"
				source.MediaStreams[0].ColorTransfer = &transfer
				return source
			}(),
			request:          hlsMasterCopyRequest("h264"),
			wantSDRCompanion: true,
			wantVariantCount: 2,
		},
		{
			name:             "SDR copy gets no H264 companion",
			source:           progressiveCopySource("/media/sdr-copy-feature.mkv"),
			request:          hlsMasterCopyRequest("h264"),
			wantVariantCount: 1,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := newHLSService(&progressiveFakeSource{source: test.source}, &hlsFakeManager{})
			response, err := service.Master(t.Context(), test.request)
			if test.wantError {
				if !errors.Is(err, playback.ErrProgressiveUnsupported) {
					t.Fatalf("Master() error = %v, want ErrProgressiveUnsupported", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Master(): %v", err)
			}
			playlist := readHLSResponse(t, response)
			gotDirectDOVI := strings.Contains(playlist, "CODECS=\"dvh1.05.09,mp4a.40.2\"")
			if gotDirectDOVI != test.wantDirectDOVI {
				t.Fatalf("Master() direct Dolby variant = %t, want %t; playlist = %q", gotDirectDOVI, test.wantDirectDOVI, playlist)
			}
			gotSDRCompanion := strings.Contains(playlist, "main.m3u8?VideoCodec=h264&AllowVideoStreamCopy=false")
			if gotSDRCompanion != test.wantSDRCompanion {
				t.Fatalf("Master() SDR companion = %t, want %t; playlist = %q", gotSDRCompanion, test.wantSDRCompanion, playlist)
			}
			if test.wantVideoRange != "" && !strings.Contains(playlist, test.wantVideoRange) {
				t.Fatalf("Master() video range missing %q; playlist = %q", test.wantVideoRange, playlist)
			}
			if got := strings.Count(playlist, "#EXT-X-STREAM-INF:"); got != test.wantVariantCount {
				t.Fatalf("Master() variants = %d, want %d; playlist = %q", got, test.wantVariantCount, playlist)
			}
		})
	}
}

func TestHLSServiceMasterCopiesDolbyBaseLayersForMatchingRequestedRanges(t *testing.T) {
	for _, test := range []struct {
		name           string
		compatibility  int
		requestedRange string
		wantVideoRange string
	}{
		{name: "HDR10 base layer", compatibility: 1, requestedRange: "HDR10", wantVideoRange: "VIDEO-RANGE=PQ"},
		{name: "HLG base layer", compatibility: 4, requestedRange: "HLG", wantVideoRange: "VIDEO-RANGE=HLG"},
		// An SDR-only list copies no Dolby Vision source, not even an SDR
		// base layer (EncodingHelper.cs:2484-2488).
		{name: "SDR base layer", compatibility: 2, requestedRange: "SDR,HLG", wantVideoRange: "VIDEO-RANGE=SDR"},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := progressiveCopySource("/media/dolby-base-layer-feature.mkv")
			video := &source.MediaStreams[0]
			video.Codec = "hevc"
			profileName, bitDepth := "Main 10", 10
			profile, level, rpu, base := 8, 6, 1, 1
			video.Profile, video.BitDepth = &profileName, &bitDepth
			video.DvProfile, video.DvLevel = &profile, &level
			video.RpuPresentFlag, video.BlPresentFlag, video.DvBlSignalCompatibilityID = &rpu, &base, &test.compatibility

			request := hlsMasterCopyRequest("hevc")
			request.StreamOptions = playback.NewStreamOptions()
			request.StreamOptions.Set("hevc-rangetype", test.requestedRange)
			response, err := newHLSService(&progressiveFakeSource{source: source}, &hlsFakeManager{}).Master(t.Context(), request)
			if err != nil {
				t.Fatalf("Master(): %v", err)
			}
			playlist := readHLSResponse(t, response)
			if !strings.Contains(playlist, "CODECS=\"hvc1") || !strings.Contains(playlist, test.wantVideoRange) {
				t.Fatalf("Master() playlist = %q, want copied HEVC with %s", playlist, test.wantVideoRange)
			}
		})
	}
}

// TestHLSServiceCopiesDolbyEnhancementLayerAsHVC1 pins D-65: a profile 7
// copy writes an hvc1 init with no Dolby Vision configuration, to match the
// hvc1 master variant, even when the client lists DOVI. Profile 8.1 keeps
// the dvh1 tag.
func TestHLSServiceCopiesDolbyEnhancementLayerAsHVC1(t *testing.T) {
	// Swiftfin AVPlayer hevc-rangetype (tools/qamatrix avPlayerRanges).
	const swiftfinRanges = "SDR,DOVIWithSDR,HLG,DOVIWithHLG,HDR10,HDR10Plus,DOVIWithHDR10,DOVIWithHDR10Plus,DOVIWithEL,DOVIWithELHDR10Plus,DOVI"
	for _, test := range []struct {
		name          string
		profile       int
		compatibility int
		hdr10Plus     bool
		wantTag       string
		wantStrict    bool
	}{
		{name: "profile 7 EL", profile: 7, compatibility: 6, wantTag: "hvc1"},
		{name: "profile 7 EL with HDR10+", profile: 7, compatibility: 6, hdr10Plus: true, wantTag: "hvc1"},
		{name: "profile 8.1 keeps dvh1", profile: 8, compatibility: 1, wantTag: "dvh1", wantStrict: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := progressiveCopySource("/media/dolby-enhancement-layer-feature.mkv")
			video := &source.MediaStreams[0]
			video.Codec = "hevc"
			profileName, bitDepth, transfer := "Main 10", 10, "smpte2084"
			level, rpu, base := 6, 1, 1
			video.Profile, video.BitDepth, video.ColorTransfer = &profileName, &bitDepth, &transfer
			video.DvProfile, video.DvLevel = &test.profile, &level
			video.RpuPresentFlag, video.BlPresentFlag, video.DvBlSignalCompatibilityID = &rpu, &base, &test.compatibility
			video.Hdr10PlusPresentFlag = &test.hdr10Plus
			videoLevel := float64(153)
			video.Level = &videoLevel

			request := hlsMasterCopyRequest("hevc")
			request.StreamOptions = playback.NewStreamOptions()
			request.StreamOptions.Set("hevc-rangetype", swiftfinRanges)
			manager := &hlsFakeManager{segment: "segment"}
			service := newHLSService(&progressiveFakeSource{source: source}, manager)
			master, err := service.Master(t.Context(), request)
			if err != nil {
				t.Fatalf("Master(): %v", err)
			}
			if playlist := readHLSResponse(t, master); !strings.Contains(playlist, `CODECS="hvc1.2.4.L153.B0`) || strings.Contains(playlist, `,CODECS="dvh1`) {
				t.Fatalf("Master() playlist = %q, want only the hvc1 base-layer variant", playlist)
			}

			request.SegmentIndex = 0
			if _, err := service.Segment(t.Context(), request); err != nil {
				t.Fatalf("Segment(0): %v", err)
			}
			args := manager.lastRestart.Args
			if !hlsArgPair(args, "-codec:v:0", "copy") || !hlsArgPair(args, "-tag:v:0", test.wantTag) {
				t.Fatalf("HLS argv = %q, want video copy tagged %s", args, test.wantTag)
			}
			if got := hlsArgPair(args, "-strict", "-2"); got != test.wantStrict {
				t.Fatalf("HLS argv = %q, -strict -2 = %t, want %t", args, got, test.wantStrict)
			}
		})
	}
}

// TestHLSServiceCopiesDolbyEnhancementLayerAsHDR10 pins deadfin#586: a
// client that lists HDR10 but not DOVIWithEL gets a profile 7 copy with the
// Dolby Vision RPU and enhancement layer removed, as an hvc1 PQ variant
// (EncodingHelper.cs:1436-1437, 2507-2520). Without a removal filter in
// ffmpeg the copy is refused (EncodingHelper.cs:1468-1479).
func TestHLSServiceCopiesDolbyEnhancementLayerAsHDR10(t *testing.T) {
	// SharkParty AVPlayer hevc-rangetype without the profile 7 entries.
	const hdr10Ranges = "SDR,DOVIWithSDR,HLG,DOVIWithHLG,HDR10,HDR10Plus,DOVIWithHDR10,DOVIWithHDR10Plus,DOVI"
	for _, test := range []struct {
		name       string
		options    []string
		hdr10Plus  bool
		wantCopy   bool
		wantFilter string
	}{
		{name: "hevc_metadata", options: []string{"dovi_rpu=strip", "hevc_metadata=remove_dovi"}, wantCopy: true, wantFilter: "hevc_mp4toannexb,hevc_metadata=remove_dovi=1"},
		{name: "dovi_rpu", options: []string{"dovi_rpu=strip"}, wantCopy: true, wantFilter: "hevc_mp4toannexb,dovi_rpu=strip=1"},
		{name: "HDR10+ keeps HDR10+", options: []string{"hevc_metadata=remove_dovi"}, hdr10Plus: true, wantCopy: true, wantFilter: "hevc_mp4toannexb,hevc_metadata=remove_dovi=1"},
		{name: "no removal filter", wantCopy: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := progressiveCopySource("/media/dolby-enhancement-layer-hdr10.mkv")
			video := &source.MediaStreams[0]
			video.Codec = "hevc"
			profileName, bitDepth, transfer := "Main 10", 10, "smpte2084"
			profile, level, rpu, base, compatibility := 7, 6, 1, 1, 6
			video.Profile, video.BitDepth, video.ColorTransfer = &profileName, &bitDepth, &transfer
			video.DvProfile, video.DvLevel = &profile, &level
			video.RpuPresentFlag, video.BlPresentFlag, video.DvBlSignalCompatibilityID = &rpu, &base, &compatibility
			video.Hdr10PlusPresentFlag = &test.hdr10Plus
			videoLevel := float64(153)
			video.Level = &videoLevel
			nal := "4"
			video.NalLengthSize = &nal

			request := hlsMasterCopyRequest("hevc")
			request.VideoCodec, request.VideoCodecs = "hevc,h264", []string{"hevc", "h264"}
			request.StreamOptions = playback.NewStreamOptions()
			request.StreamOptions.Set("hevc-rangetype", hdr10Ranges)
			manager := &hlsFakeManager{segment: "segment"}
			config := hlsServiceConfig(&progressiveFakeSource{source: source}, manager)
			config.Capabilities.BSFOptions = test.options
			service := playback.NewHLSService(config)
			master, err := service.Master(t.Context(), request)
			if err != nil {
				t.Fatalf("Master(): %v", err)
			}
			playlist := readHLSResponse(t, master)
			gotCopy := strings.Contains(playlist, `CODECS="hvc1.2.4.L153.B0`) && strings.Contains(playlist, "VIDEO-RANGE=PQ")
			if gotCopy != test.wantCopy || strings.Contains(playlist, "dvh1") {
				t.Fatalf("Master() playlist = %q, want hvc1 PQ copy = %t and no dvh1", playlist, test.wantCopy)
			}
			if gotHDR10Plus := strings.Contains(playlist, "/cdm4"); gotHDR10Plus != (test.wantCopy && test.hdr10Plus) {
				t.Fatalf("Master() playlist = %q, HDR10+ supplemental codec = %t", playlist, gotHDR10Plus)
			}

			request.SegmentIndex = 0
			if _, err := service.Segment(t.Context(), request); err != nil {
				t.Fatalf("Segment(0): %v", err)
			}
			args := manager.lastRestart.Args
			if got := hlsArgPair(args, "-codec:v:0", "copy"); got != test.wantCopy {
				t.Fatalf("HLS argv = %q, video copy = %t, want %t", args, got, test.wantCopy)
			}
			if !test.wantCopy {
				return
			}
			if !hlsArgPair(args, "-tag:v:0", "hvc1") || !hlsArgPair(args, "-bsf:v", test.wantFilter) || hlsArgPair(args, "-strict", "-2") {
				t.Fatalf("HLS argv = %q, want hvc1 copy with -bsf:v %s and no -strict -2", args, test.wantFilter)
			}
		})
	}
}

// A profile 8.1 HDR10+ copy for a client that lists DOVI strips HDR10+
// (EncodingHelper.cs:1463-1465) when ffmpeg has the filter, and stays a
// plain copy when it does not.
func TestHLSServiceStripsHDR10PlusForDolbyVisionClientWhenSupported(t *testing.T) {
	for _, test := range []struct {
		name       string
		options    []string
		wantFilter string
	}{
		{name: "filter", options: []string{"hevc_metadata=remove_hdr10plus"}, wantFilter: "hevc_mp4toannexb,hevc_metadata=remove_hdr10plus=1"},
		{name: "no filter", wantFilter: "hevc_mp4toannexb"},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := progressiveCopySource("/media/dolby-hdr10plus-feature.mkv")
			video := &source.MediaStreams[0]
			video.Codec = "hevc"
			profileName, bitDepth, transfer, hdr10Plus, nal := "Main 10", 10, "smpte2084", true, "4"
			profile, level, rpu, base, compatibility := 8, 6, 1, 1, 1
			video.Profile, video.BitDepth, video.ColorTransfer, video.Hdr10PlusPresentFlag, video.NalLengthSize = &profileName, &bitDepth, &transfer, &hdr10Plus, &nal
			video.DvProfile, video.DvLevel = &profile, &level
			video.RpuPresentFlag, video.BlPresentFlag, video.DvBlSignalCompatibilityID = &rpu, &base, &compatibility

			request := hlsMasterCopyRequest("hevc")
			request.StreamOptions = playback.NewStreamOptions()
			request.StreamOptions.Set("hevc-rangetype", "HDR10,DOVIWithHDR10Plus,DOVI")
			manager := &hlsFakeManager{segment: "segment"}
			config := hlsServiceConfig(&progressiveFakeSource{source: source}, manager)
			config.Capabilities.BSFOptions = test.options
			service := playback.NewHLSService(config)
			request.SegmentIndex = 0
			if _, err := service.Segment(t.Context(), request); err != nil {
				t.Fatalf("Segment(0): %v", err)
			}
			args := manager.lastRestart.Args
			if !hlsArgPair(args, "-codec:v:0", "copy") || !hlsArgPair(args, "-bsf:v", test.wantFilter) {
				t.Fatalf("HLS argv = %q, want copy with -bsf:v %s", args, test.wantFilter)
			}
		})
	}
}

func TestHLSServiceMasterIncludesSelectedHLSSubtitlesWithoutManifestFlag(t *testing.T) {
	for _, test := range []struct {
		name              string
		delivery          playback.SubtitleDeliveryMethod
		wantRendition     bool
		wantSubtitleGroup bool
	}{
		{name: "HLS delivery", delivery: playback.SubtitleHLS, wantRendition: true, wantSubtitleGroup: true},
		{name: "encode delivery", delivery: playback.SubtitleEncode},
		{name: "drop delivery", delivery: playback.SubtitleDrop},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := progressiveCopySource("/media/selected-subtitle-feature.mp4")
			language := "eng"
			source.MediaStreams = append(source.MediaStreams, playback.MediaStream{Type: playback.StreamTypeSubtitle, Index: 2, Codec: "srt", Language: &language, Path: "/media/selected-subtitle.srt"})
			selected := 2
			request := hlsTranscodeRequest()
			request.EnableSubtitlesInManifest = false
			request.APIKey = "subtitle-token"
			request.SubtitleStreamIndex = &selected
			request.SubtitleDeliveryMethod = &test.delivery

			response, err := newHLSService(&progressiveFakeSource{source: source}, &hlsFakeManager{}).Master(t.Context(), request)
			if err != nil {
				t.Fatalf("Master(): %v", err)
			}
			playlist := readHLSResponse(t, response)
			gotRendition := strings.Contains(playlist, "#EXT-X-MEDIA:TYPE=SUBTITLES") && strings.Contains(playlist, "URI=\""+request.MediaSourceID+"/Subtitles/2/subtitles.m3u8?SegmentLength=30&ApiKey=subtitle-token\"")
			if gotRendition != test.wantRendition {
				t.Fatalf("Master() subtitle rendition = %t, want %t; playlist = %q", gotRendition, test.wantRendition, playlist)
			}
			gotSubtitleGroup := strings.Contains(playlist, "SUBTITLES=\"subs\"")
			if gotSubtitleGroup != test.wantSubtitleGroup {
				t.Fatalf("Master() subtitle group = %t, want %t; playlist = %q", gotSubtitleGroup, test.wantSubtitleGroup, playlist)
			}
		})
	}
}

func hlsMasterCopyRequest(codec string) playback.HLSRequest {
	request := hlsTranscodeRequest()
	request.VideoCodec, request.VideoCodecs = codec, []string{codec}
	request.VideoBitrate = hlsInt(3_000_000)
	request.AllowVideoStreamCopy = true
	request.EnableAutoStreamCopy = true
	if codec == "hevc" {
		request.SegmentContainer = "fmp4"
	}
	return request
}

func hlsMasterDOVIRequest() playback.HLSRequest {
	request := hlsMasterCopyRequest("hevc")
	request.StreamOptions = playback.NewStreamOptions()
	request.StreamOptions.Set("hevc-rangetype", "DOVI")
	return request
}

func hlsFloat32(value float32) *float32 { return &value }

func hlsConstraintArgumentValue(args []string, option string) string {
	for index := 0; index+1 < len(args); index++ {
		if args[index] == option {
			return args[index+1]
		}
	}
	return ""
}

func TestHLSServiceConstrainedH264ProfileAndLevelMatchMasterAndEncoder(t *testing.T) {
	for _, test := range []struct {
		name, profile, level, codecToken, encoderProfile, encoderLevel string
	}{
		{name: "High level 4.1", profile: "High", level: "4.1", codecToken: "avc1.640029", encoderProfile: "high", encoderLevel: "4.1"},
		{name: "Main level 4.0", profile: "Main", level: "4.0", codecToken: "avc1.4D4028", encoderProfile: "main", encoderLevel: "4.0"},
		{name: "Baseline level 4.0", profile: "Baseline", level: "4.0", codecToken: "avc1.42E028", encoderProfile: "baseline", encoderLevel: "4.0"},
		// Swiftfin sends h264-level=80; NormalizeTranscodingLevel caps it at 5.1.
		{name: "Swiftfin level 80", profile: "high,main,baseline,constrainedbaseline", level: "80", codecToken: "avc1.640033", encoderProfile: "high", encoderLevel: "5.1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager := &hlsFakeManager{segment: "encoded segment"}
			service := newHLSService(&progressiveFakeSource{source: progressiveCopySource("/media/constrained-" + test.profile + ".mkv")}, manager)
			request := hlsTranscodeRequest()
			profile, level := test.profile, test.level
			request.Profile, request.Level = &profile, &level

			response, err := service.Master(t.Context(), request)
			if err != nil {
				t.Fatalf("Master(): %v", err)
			}
			playlist := readHLSResponse(t, response)
			if !strings.Contains(playlist, "CODECS=\""+test.codecToken+",") {
				t.Fatalf("Master() profile=%s level=%s playlist = %q, want RFC 6381 token %q", test.profile, test.level, playlist, test.codecToken)
			}

			if _, err := service.Segment(t.Context(), request); err != nil {
				t.Fatalf("Segment(): %v", err)
			}
			if got := hlsConstraintArgumentValue(manager.lastRestart.Args, "-profile:v:0"); got != test.encoderProfile {
				t.Fatalf("Segment() profile argv = %q, want constrained profile %q; argv=%q", got, test.encoderProfile, manager.lastRestart.Args)
			}
			if got := hlsConstraintArgumentValue(manager.lastRestart.Args, "-level:v:0"); got != test.encoderLevel {
				t.Fatalf("Segment() level argv = %q, want constrained level %q; argv=%q", got, test.encoderLevel, manager.lastRestart.Args)
			}
		})
	}
}

// TestHLSServiceUnconstrainedH264LevelFitsOutput pins D-66: without a
// requested level, the master CODECS token and the encoder -level carry the
// lowest level that holds the output, not below 4.1.
func TestHLSServiceUnconstrainedH264LevelFitsOutput(t *testing.T) {
	for _, test := range []struct {
		name                      string
		width, height             int
		frameRate                 float32
		maxWidth, maxHeight, rate int
		codecToken, encoderLevel  string
	}{
		{name: "720p30 stays 4.1", width: 1280, height: 720, frameRate: 30, maxWidth: 1920, maxHeight: 1080, rate: 3_000_000, codecToken: "avc1.640029", encoderLevel: "4.1"},
		{name: "1440p24 needs 5.0", width: 3840, height: 2160, frameRate: 23.976, maxWidth: 2560, maxHeight: 1440, rate: 19_360_000, codecToken: "avc1.640032", encoderLevel: "5.0"},
		{name: "2160p24 needs 5.1", width: 3840, height: 2160, frameRate: 23.976, maxWidth: 3840, maxHeight: 2160, rate: 60_000_000, codecToken: "avc1.640033", encoderLevel: "5.1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := progressiveCopySource("/media/unconstrained-level.mkv")
			video := &source.MediaStreams[0]
			video.Width, video.Height = &test.width, &test.height
			video.AverageFrameRate, video.RealFrameRate = &test.frameRate, &test.frameRate
			bitrate := 50_000_000
			video.BitRate = &bitrate
			manager := &hlsFakeManager{segment: "encoded segment"}
			service := newHLSService(&progressiveFakeSource{source: source}, manager)
			request := hlsTranscodeRequest()
			request.Profile, request.Level = nil, nil
			request.MaxWidth, request.MaxHeight = &test.maxWidth, &test.maxHeight
			request.VideoBitrate = hlsInt(test.rate)

			response, err := service.Master(t.Context(), request)
			if err != nil {
				t.Fatalf("Master(): %v", err)
			}
			if playlist := readHLSResponse(t, response); !strings.Contains(playlist, `CODECS="`+test.codecToken+",") {
				t.Fatalf("Master() playlist = %q, want %s", playlist, test.codecToken)
			}
			if _, err := service.Segment(t.Context(), request); err != nil {
				t.Fatalf("Segment(): %v", err)
			}
			if got := hlsConstraintArgumentValue(manager.lastRestart.Args, "-level:v:0"); got != test.encoderLevel {
				t.Fatalf("Segment() level argv = %q, want %q; argv=%q", got, test.encoderLevel, manager.lastRestart.Args)
			}
		})
	}
}

// TestHLSMeasuredConstraints verifies the measured RC2 constraints: bitrate
// pass-through, the video-only HLS path, the measured channel rule, the
// measured HDR rule, and the measured segment-time rule. The pinned
// ResolutionNormalizer@v1 defines the resolution cap; the RC2 observations
// confirm source-independence only.
func TestHLSMeasuredConstraintsVideoOnlyEmitsMaxrateBufsizeAndNoAudioArgs(t *testing.T) {
	source := progressiveCopySource("/media/video-only-hlg.mkv")
	source.MediaStreams = source.MediaStreams[:1]
	video := &source.MediaStreams[0]
	transfer := "arib-std-b67"
	bitDepth := 10
	video.Codec = "hevc"
	video.ColorTransfer = &transfer
	video.BitDepth = &bitDepth

	manager := &hlsFakeManager{segment: "segment"}
	service := newHLSService(&progressiveFakeSource{source: source}, manager)
	request := hlsTranscodeRequest()
	request.VideoBitrate = hlsInt(64_000)
	request.AudioBitrate = nil
	request.AudioStreamIndex = nil
	request.MaxWidth = nil
	request.MaxHeight = nil
	request.SegmentLengthSeconds = 0
	request.SegmentLengthSet = false
	request.AllowVideoStreamCopy = false
	request.AllowAudioStreamCopy = false
	request.EnableAutoStreamCopy = false
	request.AudioCodecs = []string{"aac"}

	if _, err := service.Segment(t.Context(), request); err != nil {
		t.Fatalf("Segment(): %v", err)
	}
	args := manager.lastRestart.Args

	if !hlsArgPair(args, "-maxrate", "64000") {
		t.Errorf("video-only 64k: argv = %q, want -maxrate 64000", args)
	}
	if !hlsArgPair(args, "-bufsize", "128000") {
		t.Errorf("video-only 64k: argv = %q, want -bufsize 128000 (2x maxrate)", args)
	}
	for _, option := range []string{"-codec:a:0", "-ac", "-ab", "-ar"} {
		if hlsHasArg(args, option) {
			t.Errorf("video-only 64k: argv = %q, must not emit audio encoder arg %s", args, option)
		}
	}
	if !hlsArgSequence(args, "-map", "-0:a") {
		t.Errorf("video-only 64k: argv = %q, want -map -0:a to exclude audio", args)
	}
	if !hlsArgPair(args, "-hls_time", "3") {
		t.Errorf("video-only 64k: argv = %q, want -hls_time 3 for encoded video", args)
	}
}

func TestHLSMeasuredConstraintsVideoWithAudioEmitsMaxrateBufsize(t *testing.T) {
	source := progressiveCopySource("/media/mono-feature.mp4")
	video := &source.MediaStreams[0]
	_ = video

	manager := &hlsFakeManager{segment: "segment"}
	service := newHLSService(&progressiveFakeSource{source: source}, manager)
	request := hlsTranscodeRequest()
	request.VideoBitrate = hlsInt(292_000)
	request.AudioBitrate = hlsInt(128_000)
	request.MaxWidth = nil
	request.MaxHeight = nil
	request.AllowVideoStreamCopy = false
	request.AllowAudioStreamCopy = false
	request.EnableAutoStreamCopy = false

	if _, err := service.Segment(t.Context(), request); err != nil {
		t.Fatalf("Segment(): %v", err)
	}
	args := manager.lastRestart.Args

	if !hlsArgPair(args, "-maxrate", "292000") {
		t.Errorf("292k video: argv = %q, want -maxrate 292000", args)
	}
	if !hlsArgPair(args, "-bufsize", "584000") {
		t.Errorf("292k video: argv = %q, want -bufsize 584000 (2x maxrate)", args)
	}
}

// TestHLSMeasuredConstraints420kEmitsMaxrateBufsize verifies that a 420000
// bps HLS job retains the requested bitrate as -maxrate. The
// h264EquivalentResolutionBitrateV1 conversion scales the bitrate for the
// resolution normalizer only; the ffmpeg rate-control argument stays at the
// value the client sent.
func TestHLSMeasuredConstraints420kEmitsMaxrateBufsize(t *testing.T) {
	source := progressiveCopySource("/media/420k-feature.mp4")

	manager := &hlsFakeManager{segment: "segment"}
	service := newHLSService(&progressiveFakeSource{source: source}, manager)
	request := hlsTranscodeRequest()
	request.VideoBitrate = hlsInt(420_000)
	request.AudioBitrate = hlsInt(128_000)
	request.MaxWidth = nil
	request.MaxHeight = nil
	request.AllowVideoStreamCopy = false
	request.AllowAudioStreamCopy = false
	request.EnableAutoStreamCopy = false

	if _, err := service.Segment(t.Context(), request); err != nil {
		t.Fatalf("Segment(): %v", err)
	}
	args := manager.lastRestart.Args

	if !hlsArgPair(args, "-maxrate", "420000") {
		t.Errorf("420k video: argv = %q, want -maxrate 420000", args)
	}
	if !hlsArgPair(args, "-bufsize", "840000") {
		t.Errorf("420k video: argv = %q, want -bufsize 840000 (2x maxrate)", args)
	}
}

func TestHLSMeasuredConstraintsEncodedAudioBitratePassthrough(t *testing.T) {
	source := progressiveCopySource("/media/multichannel-feature.mp4")

	manager := &hlsFakeManager{segment: "segment"}
	service := newHLSService(&progressiveFakeSource{source: source}, manager)
	request := hlsTranscodeRequest()
	request.VideoBitrate = hlsInt(292_000)
	request.AudioBitrate = hlsInt(128_000)
	request.MaxWidth = nil
	request.MaxHeight = nil
	request.AllowVideoStreamCopy = false
	request.AllowAudioStreamCopy = false
	request.EnableAutoStreamCopy = false

	if _, err := service.Segment(t.Context(), request); err != nil {
		t.Fatalf("Segment(): %v", err)
	}
	args := manager.lastRestart.Args

	if !hlsArgPair(args, "-ab", "128000") {
		t.Errorf("encoded audio 128k: argv = %q, want -ab 128000", args)
	}
	// The audio bitrate is the requested value, not derived from the channel
	// count. The source has 6 channels but the bitrate stays at 128000.
	if bit := hlsConstraintArgumentValue(args, "-ab"); bit != "128000" {
		t.Errorf("encoded audio 128k: -ab = %q, want 128000 (not channel-derived)", bit)
	}
}

func TestHLSMeasuredConstraintsChannelCountFollowsSource(t *testing.T) {
	for _, tc := range []struct {
		name        string
		channels    int
		maxChannels *int
		wantAC      string
	}{
		{name: "mono source emits -ac 1", channels: 1, maxChannels: hlsInt(2), wantAC: "1"},
		{name: "6-channel under 8-channel cap emits -ac 6", channels: 6, maxChannels: hlsInt(8), wantAC: "6"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := progressiveCopySource("/media/channel-test.mp4")
			source.MediaStreams[1].Channels = &tc.channels

			manager := &hlsFakeManager{segment: "segment"}
			service := newHLSService(&progressiveFakeSource{source: source}, manager)
			request := hlsTranscodeRequest()
			request.VideoBitrate = hlsInt(292_000)
			request.AudioBitrate = hlsInt(128_000)
			request.MaxAudioChannels = tc.maxChannels
			request.MaxWidth = nil
			request.MaxHeight = nil
			request.AllowVideoStreamCopy = false
			request.AllowAudioStreamCopy = false
			request.EnableAutoStreamCopy = false

			if _, err := service.Segment(t.Context(), request); err != nil {
				t.Fatalf("Segment(): %v", err)
			}
			args := manager.lastRestart.Args
			if !hlsArgPair(args, "-ac", tc.wantAC) {
				t.Errorf("channels %d cap %v: argv = %q, want -ac %s", tc.channels, tc.maxChannels, args, tc.wantAC)
			}
		})
	}
}

func TestHLSMeasuredConstraintsHDRToneMapping(t *testing.T) {
	for _, tc := range []struct {
		name     string
		transfer string
		wantSet  string
	}{
		{name: "HLG source", transfer: "arib-std-b67", wantSet: "setparams=color_primaries=bt2020:color_trc=arib-std-b67:colorspace=bt2020nc"},
		{name: "PQ source", transfer: "smpte2084", wantSet: "setparams=color_primaries=bt2020:color_trc=smpte2084:colorspace=bt2020nc"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := progressiveCopySource("/media/hdr-feature.mkv")
			video := &source.MediaStreams[0]
			video.Codec = "hevc"
			video.ColorTransfer = &tc.transfer
			bitDepth := 10
			video.BitDepth = &bitDepth

			manager := &hlsFakeManager{segment: "segment"}
			service := newHLSService(&progressiveFakeSource{source: source}, manager)
			request := hlsTranscodeRequest()
			request.VideoBitrate = hlsInt(64_000)
			request.AudioBitrate = hlsInt(128_000)
			request.MaxWidth = nil
			request.MaxHeight = nil
			request.AllowVideoStreamCopy = false
			request.AllowAudioStreamCopy = false
			request.EnableAutoStreamCopy = false

			if _, err := service.Segment(t.Context(), request); err != nil {
				t.Fatalf("Segment(): %v", err)
			}
			args := manager.lastRestart.Args
			filter := hlsConstraintArgumentValue(args, "-vf")

			// Both HLG and PQ emit the same bt2390 tone-map operator.
			wantTonemap := "tonemapx=tonemap=bt2390:desat=0:peak=100:t=bt709:m=bt709:p=bt709:format=yuv420p"
			if !strings.Contains(filter, wantTonemap) {
				t.Errorf("%s: -vf = %q, want tonemapx bt2390 operator", tc.name, filter)
			}

			// The input transfer name is preserved in setparams.
			if !strings.Contains(filter, tc.wantSet) {
				t.Errorf("%s: -vf = %q, want setparams with %s transfer", tc.name, filter, tc.transfer)
			}
		})
	}
}

func TestHLSMeasuredConstraintsSegmentTime(t *testing.T) {
	for _, tc := range []struct {
		name      string
		copyVideo bool
		wantHLS   string
	}{
		{name: "encoded video emits -hls_time 3", copyVideo: false, wantHLS: "3"},
		{name: "video copy emits -hls_time 6", copyVideo: true, wantHLS: "6"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := progressiveCopySource("/media/segment-test.mp4")

			manager := &hlsFakeManager{segment: "segment"}
			service := newHLSService(&progressiveFakeSource{source: source}, manager)
			request := hlsTranscodeRequest()
			request.AudioBitrate = hlsInt(128_000)
			request.MaxWidth = nil
			request.MaxHeight = nil
			request.SegmentLengthSeconds = 0
			request.SegmentLengthSet = false
			request.AllowVideoStreamCopy = tc.copyVideo
			request.AllowAudioStreamCopy = tc.copyVideo
			request.EnableAutoStreamCopy = tc.copyVideo
			if tc.copyVideo {
				request.VideoBitrate = nil
			} else {
				request.VideoBitrate = hlsInt(292_000)
			}

			if _, err := service.Segment(t.Context(), request); err != nil {
				t.Fatalf("Segment(): %v", err)
			}
			args := manager.lastRestart.Args
			if !hlsArgPair(args, "-hls_time", tc.wantHLS) {
				t.Errorf("%s: argv = %q, want -hls_time %s", tc.name, args, tc.wantHLS)
			}
		})
	}
}

func TestHLSMeasuredConstraintsResolutionCapSourceIndependent(t *testing.T) {
	// Two sources with different dimensions and identical normalization
	// inputs receive the same cap. RC2 observed 960 for both a 320x180 and a
	// 3840x2160 source at the same bitrate. The HLS caller converts the
	// requested bitrate to its H.264-equivalent value through
	// h264EquivalentResolutionBitrateV1 before the normalizer selects the
	// breakpoint. At 292000 bps the H.264-equivalent is 1168000, and the
	// 30 fps source frame rate gives a 1168000 reference, which selects the
	// pinned 960 breakpoint.
	//
	// Both sources must be wider than the cap of 960 so the cap applies to
	// both. A source at or below the cap keeps its native width, which does
	// not prove the cap is source-independent.
	wantScale := "scale=trunc(960/2)*2:trunc(540/2)*2"
	for _, dims := range []struct {
		name          string
		width, height int
	}{
		{name: "1920x1080", width: 1920, height: 1080},
		{name: "3840x2160", width: 3840, height: 2160},
	} {
		t.Run(dims.name, func(t *testing.T) {
			source := progressiveCopySource("/media/source-" + dims.name + ".mkv")
			source.MediaStreams[0].Width = &dims.width
			source.MediaStreams[0].Height = &dims.height

			manager := &hlsFakeManager{segment: "segment"}
			service := newHLSService(&progressiveFakeSource{source: source}, manager)
			request := hlsTranscodeRequest()
			request.VideoBitrate = hlsInt(292_000)
			request.AudioBitrate = hlsInt(128_000)
			request.MaxWidth = nil
			request.MaxHeight = nil
			request.AllowVideoStreamCopy = false
			request.AllowAudioStreamCopy = false
			request.EnableAutoStreamCopy = false

			if _, err := service.Segment(t.Context(), request); err != nil {
				t.Fatalf("Segment(): %v", err)
			}
			filter := hlsConstraintArgumentValue(manager.lastRestart.Args, "-vf")
			if !strings.Contains(filter, wantScale) {
				t.Errorf("%s: -vf = %q, want scale %s (v1 cap 960 applied to both sources)", dims.name, filter, wantScale)
			}
		})
	}
}

// TestHLSMeasuredConstraintsVideoOnlyMasterNoPanic verifies that a video-only
// source (no audio stream) builds a master playlist without an error. The
// master variant CODECS attribute contains exactly one RFC 6381 video codec
// token and no audio token.
func TestHLSMeasuredConstraintsVideoOnlyMasterNoPanic(t *testing.T) {
	source := progressiveCopySource("/media/video-only-master.mkv")
	source.MediaStreams = source.MediaStreams[:1]
	video := &source.MediaStreams[0]
	video.Codec = "h264"

	manager := &hlsFakeManager{segment: "segment"}
	service := newHLSService(&progressiveFakeSource{source: source}, manager)
	request := hlsTranscodeRequest()
	request.VideoBitrate = hlsInt(64_000)
	request.AudioBitrate = nil
	request.AudioStreamIndex = nil
	request.MaxWidth = nil
	request.MaxHeight = nil
	request.AllowVideoStreamCopy = false
	request.AllowAudioStreamCopy = false
	request.EnableAutoStreamCopy = false
	request.AudioCodecs = []string{"aac"}

	response, err := service.Master(t.Context(), request)
	if err != nil {
		t.Fatalf("Master(): %v", err)
	}
	playlist := readHLSResponse(t, response)
	if !strings.Contains(playlist, "#EXTM3U") {
		t.Fatalf("video-only master: playlist = %q, want #EXTM3U", playlist)
	}
	// The master variant CODECS attribute contains exactly one RFC 6381
	// video codec token. The renderer emits avc1.640029 for H.264 High@4.1
	// and writes the plural CODECS attribute. A video-only source has no
	// audio token, so the value has no comma.
	videoToken := "CODECS=\"avc1.640029\""
	if !strings.Contains(playlist, videoToken) {
		t.Errorf("video-only master: playlist = %q, must contain %s (one video token, no comma)", playlist, videoToken)
	}
	// A video-only source has no audio codec to project. The master
	// playlist must not carry an audio token such as mp4a.40.2 or a
	// copy sentinel. These use the exact spellings the renderer emits.
	if strings.Contains(playlist, "mp4a.40.2") {
		t.Errorf("video-only master: playlist = %q, must not contain mp4a.40.2 (audio token)", playlist)
	}
	if strings.Contains(playlist, "CODECS=\"copy") {
		t.Errorf("video-only master: playlist = %q, must not contain a copy sentinel", playlist)
	}
}

// TestHLSMeasuredConstraintsEffectiveSourceFrameRate verifies that when no
// request rate constrains the source, the normalizer uses the source's own
// frame rate instead of the 30 fps default. A 24 fps source at 600000 bps
// has an H.264-equivalent of 1800000 (band 3, scale 3), and the 24 fps
// reference is 2250000 (1800000 * 30/24), which selects the 960 breakpoint.
func TestHLSMeasuredConstraintsEffectiveSourceFrameRate(t *testing.T) {
	source := progressiveCopySource("/media/24fps-feature.mkv")
	video := &source.MediaStreams[0]
	fps := float32(24)
	video.AverageFrameRate = &fps

	manager := &hlsFakeManager{segment: "segment"}
	service := newHLSService(&progressiveFakeSource{source: source}, manager)
	request := hlsTranscodeRequest()
	request.VideoBitrate = hlsInt(600_000)
	request.AudioBitrate = hlsInt(128_000)
	request.MaxWidth = nil
	request.MaxHeight = nil
	request.Framerate = nil
	request.MaxFramerate = nil
	request.AllowVideoStreamCopy = false
	request.AllowAudioStreamCopy = false
	request.EnableAutoStreamCopy = false

	if _, err := service.Segment(t.Context(), request); err != nil {
		t.Fatalf("Segment(): %v", err)
	}
	filter := hlsConstraintArgumentValue(manager.lastRestart.Args, "-vf")
	wantScale := "scale=trunc(960/2)*2:trunc(540/2)*2"
	if !strings.Contains(filter, wantScale) {
		t.Errorf("24fps 600k no-rate-query: -vf = %q, want %s (source frame rate applied, not 30 fps default)", filter, wantScale)
	}
}

// TestHLSMeasuredConstraintsClearedHeightApplied verifies that when the
// normalizer changes MaxWidth and clears MaxHeight, the caller propagates
// the clear. With MaxWidth=1920 and MaxHeight=100, the normalizer caps at
// 960 (H.264-equivalent 1168000, 30 fps reference 1168000) and clears the
// height. The output must use the aspect-preserved height, not the stale
// MaxHeight=100.
func TestHLSMeasuredConstraintsClearedHeightApplied(t *testing.T) {
	source := progressiveCopySource("/media/cleared-height-feature.mkv")

	manager := &hlsFakeManager{segment: "segment"}
	service := newHLSService(&progressiveFakeSource{source: source}, manager)
	request := hlsTranscodeRequest()
	request.VideoBitrate = hlsInt(292_000)
	request.AudioBitrate = hlsInt(128_000)
	request.MaxWidth = hlsInt(1920)
	request.MaxHeight = hlsInt(100)
	request.AllowVideoStreamCopy = false
	request.AllowAudioStreamCopy = false
	request.EnableAutoStreamCopy = false

	if _, err := service.Segment(t.Context(), request); err != nil {
		t.Fatalf("Segment(): %v", err)
	}
	filter := hlsConstraintArgumentValue(manager.lastRestart.Args, "-vf")
	wantScale := "scale=trunc(960/2)*2:trunc(540/2)*2"
	if !strings.Contains(filter, wantScale) {
		t.Errorf("MaxWidth=1920 MaxHeight=100: -vf = %q, want %s (normalizer cleared height, aspect-preserved 540 not stale 100)", filter, wantScale)
	}
}

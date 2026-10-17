// SPDX-License-Identifier: GPL-2.0-only

package hls_test

// Source vectors below are ported from Jellyfin b04614e18d2aeed8d3d3de920c381c6bd5b97bdb:
// Jellyfin.Api/Helpers/HlsCodecStringHelpers.cs:53-122,171-227,300-368.

import (
	"testing"

	"github.com/deadfin-org/deadfin/internal/hls"
)

// TestVideoCodecStringMatchesPinnedRFC6381Vectors defends the exact CODECS
// tokens emitted to HLS clients for supported video streams, including Dolby
// Vision streams that need a distinct client-selectable FourCC.
func TestVideoCodecStringMatchesPinnedRFC6381Vectors(t *testing.T) {
	for _, tc := range []struct {
		name  string
		codec hls.VideoCodec
		want  string
	}{
		{
			name:  "H.264 high profile level 4.1",
			codec: hls.VideoCodec{Codec: "h264", Profile: "High", Level: 41},
			want:  "avc1.640029",
		},
		{
			name:  "H.264 main profile level 3.1",
			codec: hls.VideoCodec{Codec: "h264", Profile: "MAIN", Level: 31},
			want:  "avc1.4D401F",
		},
		{
			name:  "H.264 baseline profile level 3.0",
			codec: hls.VideoCodec{Codec: "h264", Profile: "baseline", Level: 30},
			want:  "avc1.42E01E",
		},
		{
			name:  "HEVC main 10 profile level 120",
			codec: hls.VideoCodec{Codec: "hevc", Profile: "main 10", Level: 120},
			want:  "hvc1.2.4.L120.B0",
		},
		{
			name:  "HEVC main 10 high tier profile level 153",
			codec: hls.VideoCodec{Codec: "hevc", Profile: "main 10", Level: 153, HighTier: true},
			want:  "hvc1.2.4.H153.B0",
		},
		{
			name:  "HEVC main 10 unknown tier defaults to main",
			codec: hls.VideoCodec{Codec: "hevc", Profile: "main 10", Level: 153},
			want:  "hvc1.2.4.L153.B0",
		},
		{
			name:  "HEVC main profile level 93",
			codec: hls.VideoCodec{Codec: "h265", Profile: "main", Level: 93},
			want:  "hvc1.1.4.L93.B0",
		},
		{
			name:  "AV1 main profile high tier 10 bit",
			codec: hls.VideoCodec{Codec: "av1", Profile: "Main", Level: 13, HighTier: true, BitDepth: 10},
			want:  "av01.0.13H.10",
		},
		{
			name:  "AV1 professional profile main tier 12 bit",
			codec: hls.VideoCodec{Codec: "av1", Profile: "Professional", Level: 31, BitDepth: 12},
			want:  "av01.2.31M.12",
		},
		{
			name: "Dolby Vision HEVC profile 5 level 9",
			codec: hls.VideoCodec{
				Codec: "hevc", DolbyVisionProfile: 5, DolbyVisionLevel: 9,
			},
			want: "dvh1.05.09",
		},
		{
			name: "Dolby Vision AV1 profile 10 level 13",
			codec: hls.VideoCodec{
				Codec: "AV1", DolbyVisionProfile: 10, DolbyVisionLevel: 13,
			},
			want: "dav1.10.13",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := hls.VideoCodecString(tc.codec); got != tc.want {
				t.Errorf("VideoCodecString(%+v) = %q, want %q", tc.codec, got, tc.want)
			}
		})
	}
}

// TestAudioCodecStringMatchesPinnedRFC6381Vectors defends the media-codec
// labels that HLS clients use to decide whether an audio rendition is playable.
func TestAudioCodecStringMatchesPinnedRFC6381Vectors(t *testing.T) {
	for _, tc := range []struct {
		name  string
		codec hls.AudioCodec
		want  string
	}{
		{
			name:  "HE-AAC profile comparison is case insensitive",
			codec: hls.AudioCodec{Codec: "aac", Profile: "he-aac"},
			want:  "mp4a.40.5",
		},
		{name: "MP3", codec: hls.AudioCodec{Codec: "mp3"}, want: "mp4a.40.34"},
		{name: "AC-3", codec: hls.AudioCodec{Codec: "ac3"}, want: "ac-3"},
		{name: "E-AC-3", codec: hls.AudioCodec{Codec: "eac3"}, want: "ec-3"},
		{name: "FLAC", codec: hls.AudioCodec{Codec: "flac"}, want: "fLaC"},
		{name: "ALAC", codec: hls.AudioCodec{Codec: "alac"}, want: "alac"},
		{name: "Opus", codec: hls.AudioCodec{Codec: "opus"}, want: "Opus"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := hls.AudioCodecString(tc.codec); got != tc.want {
				t.Errorf("AudioCodecString(%+v) = %q, want %q", tc.codec, got, tc.want)
			}
		})
	}
}

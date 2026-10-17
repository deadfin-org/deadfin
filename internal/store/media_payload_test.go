// SPDX-License-Identifier: GPL-2.0-only

package store

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/deadfin-org/deadfin/internal/playback"
)

// streamFromColumns decodes extra once and assigns the columns directly. It
// must return what the merged decode returns, including the errors.
func TestStreamFromColumnsMatchesMergedDecode(t *testing.T) {
	type columns struct {
		index                                    int
		streamType                               string
		codec, language, title                   any
		defaultValue, forced, external           any
		bitrate, width, height, channels, sample any
	}
	video := columns{0, "Video", "hevc", nil, nil, int64(1), int64(0), int64(0), int64(8_000_000), int64(3840), int64(2160), nil, nil}
	audio := columns{1, "Audio", "eac3", "eng", "Surround 5.1", int64(0), int64(0), int64(0), int64(640_000), nil, nil, int64(6), int64(48_000)}
	subtitle := columns{2, "Subtitle", "subrip", "ger", nil, false, true, true, nil, nil, nil, nil, nil}
	cases := []struct {
		name    string
		columns columns
		extra   string
	}{
		{"empty extra", video, ""},
		{"empty object", audio, "{}"},
		{"null columns", subtitle, `{"DisplayTitle":"German - Forced","IsTextSubtitleStream":true}`},
		{"video extra", video, `{"AverageFrameRate":23.976025,"BitDepth":10,"Level":150,"Profile":"Main 10","RealFrameRate":23.976025,"RefFrames":1,"VideoRange":"HDR","DvProfile":8}`},
		{"audio extra", audio, `{"ChannelLayout":"5.1(side)","DisplayTitle":"Surround 5.1 - English - Dolby Digital+ - Default","Profile":"Dolby Digital Plus"}`},
		{"extra names a column", audio, `{"BitRate":1,"Codec":"aac","Language":"fre"}`},
		{"extra names a column in another case", audio, `{"bitrate":1,"codec":"aac","CHANNELS":2}`},
		{"string number", video, `{"BitDepth":"10","AverageFrameRate":"23.976"}`},
		{"non-canonical number", video, `{"BitDepth":1e1}`},
		{"extra int above int32", video, `{"RefFrames":3000000000}`},
		{"extra int below int32", video, `{"RefFrames":-3000000000}`},
		{"float above float32", video, `{"AverageFrameRate":1e39}`},
		{"long digit string", audio, `{"DisplayTitle":"1234567890"}`},
		{"malformed extra", audio, `{"DisplayTitle":`},
		{"column int above int32", columns{3, "Video", "h264", nil, nil, nil, nil, nil, int64(3_000_000_000), nil, nil, nil, nil}, `{"BitDepth":8}`},
		{"index above int32", columns{3_000_000_000, "Audio", "aac", nil, nil, nil, nil, nil, nil, nil, nil, nil, nil}, ""},
		{"unknown type", columns{4, "Hologram", "x", nil, nil, nil, nil, nil, nil, nil, nil, nil, nil}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := tc.columns
			want, wantErr := mergedStreamFromColumns(c.index, c.streamType, c.codec, c.language, c.title, c.defaultValue, c.forced, c.external, c.bitrate, c.width, c.height, c.channels, c.sample, tc.extra)
			got, gotErr := streamFromColumns(c.index, c.streamType, c.codec, c.language, c.title, c.defaultValue, c.forced, c.external, c.bitrate, c.width, c.height, c.channels, c.sample, tc.extra)
			if (gotErr == nil) != (wantErr == nil) {
				t.Fatalf("error = %v, merged error = %v", gotErr, wantErr)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("stream = %+v\nmerged = %+v", got, want)
			}
		})
	}
}

// A stream written by streamExtra must read back through streamFromColumns
// without the merged decode or the wire rewrite.
func TestStreamExtraRoundTripUsesPlainDecode(t *testing.T) {
	rate := float32(23.976025)
	depth, refs := 10, 4
	level := 150.0
	profile := "Main 10"
	stream := playback.MediaStream{AverageFrameRate: &rate, BitDepth: &depth, HighTier: true, RefFrames: &refs, Level: &level, Profile: &profile}
	extra, err := streamExtra(stream)
	if err != nil {
		t.Fatalf("stream extra: %v", err)
	}
	if streamExtraNamesColumn(extra) || hasLongDigitRun(extra, 10) {
		t.Fatalf("extra %s takes the slow decode", extra)
	}
	var plain rawMediaStream
	if err := json.Unmarshal([]byte(extra), &plain); err != nil {
		t.Fatalf("plain decode of %s: %v", extra, err)
	}
	got, err := streamFromColumns(0, "Video", "hevc", nil, nil, false, false, false, nil, nil, nil, nil, nil, extra)
	if err != nil {
		t.Fatalf("stream from columns: %v", err)
	}
	if !got.HighTier {
		t.Errorf("round-tripped HighTier = false, want true")
	}
}

func BenchmarkStreamFromColumns(b *testing.B) {
	extra := `{"AverageFrameRate":23.976025,"BitDepth":10,"ColorPrimaries":"bt2020","ColorTransfer":"smpte2084","DisplayTitle":"4K HEVC HDR","IsAVC":false,"IsInterlaced":false,"Level":150,"PixelFormat":"yuv420p10le","Profile":"Main 10","RealFrameRate":23.976025,"RefFrames":1,"TimeBase":"1/1000","VideoRange":"HDR","VideoRangeType":"HDR10"}`
	decode := map[string]func() (playback.MediaStream, error){
		"direct": func() (playback.MediaStream, error) {
			return streamFromColumns(0, "Video", "hevc", nil, nil, int64(1), int64(0), int64(0), int64(8_000_000), int64(3840), int64(2160), nil, nil, extra)
		},
		"merged": func() (playback.MediaStream, error) {
			return mergedStreamFromColumns(0, "Video", "hevc", nil, nil, int64(1), int64(0), int64(0), int64(8_000_000), int64(3840), int64(2160), nil, nil, extra)
		},
	}
	for name, run := range decode {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := run(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

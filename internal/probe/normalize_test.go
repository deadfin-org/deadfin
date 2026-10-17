// SPDX-License-Identifier: GPL-2.0-only

package probe_test

// Source fixtures and expected values below are ported from Jellyfin b04614e18d2aeed8d3d3de920c381c6bd5b97bdb:
// ProbeResultNormalizerTests.cs and ProbeResultNormalizer.cs:294-339,636-689,700-1063.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/attest"
	"github.com/deadfin-org/deadfin/internal/playback"
	"github.com/deadfin-org/deadfin/internal/probe"
)

func TestPinnedProbeFixtureInventory(t *testing.T) {
	jellyport := attest.GateForTest(t)
	data, err := os.ReadFile(filepath.Join("testdata", "upstream-b04614e", "inventory.json"))
	if err != nil {
		t.Fatal(err)
	}
	var inventory struct {
		SourceCommit    string   `json:"source_commit"`
		Fixtures        []string `json:"fixtures"`
		DerivedFixtures []string `json:"derived_fixtures"`
	}
	if err := json.Unmarshal(data, &inventory); err != nil {
		t.Fatal(err)
	}
	if got, want := inventory.SourceCommit, "b04614e18d2aeed8d3d3de920c381c6bd5b97bdb"; got != want {
		t.Fatalf("source commit = %q, want %q", got, want)
	}
	if got, want := len(inventory.Fixtures), 8; got != want {
		t.Fatalf("pinned fixture count = %d, want %d", got, want)
	}
	for _, name := range append(inventory.Fixtures, inventory.DerivedFixtures...) {
		if _, err := os.Stat(filepath.Join("testdata", "upstream-b04614e", name)); err != nil {
			t.Fatalf("inventory fixture %q: %v", name, err)
		}
	}
	for _, provenance := range []string{filepath.Join("tools", "goldenrec", "fixtures", "expected-inventory.json"), filepath.Join("tools", "goldenrec", "fixtures", "source-manifest.sha256")} {
		if _, err := os.Stat(attest.AdjacentPath(jellyport, provenance)); err != nil {
			t.Fatalf("fixture provenance %q: %v", provenance, err)
		}
	}
	goldenProvenance, err := os.ReadFile(filepath.Join("testdata", "upstream-b04614e", "expected-media-info", "provenance.json"))
	if err != nil {
		t.Fatal(err)
	}
	var golden struct {
		SourceCommit string `json:"source_commit"`
	}
	if err := json.Unmarshal(goldenProvenance, &golden); err != nil {
		t.Fatal(err)
	}
	if golden.SourceCommit != inventory.SourceCommit {
		t.Fatalf("golden source commit = %q, fixture source commit = %q", golden.SourceCommit, inventory.SourceCommit)
	}
}

func TestNormalizePinnedFFProbeFixtures(t *testing.T) {
	cases := []struct {
		name           string
		fixture        string
		container      string
		checkContainer bool
		streams        int
		stream0        map[string]any
		stream1        map[string]any
	}{
		{
			name:    "metadata with Dolby Vision and spatial audio",
			fixture: "video_metadata.json", container: "mkv", checkContainer: true, streams: 3,
			stream0: map[string]any{
				"Codec": "h264", "Profile": "High 4:4:4 Predictive", "Index": float64(0),
				"Width": float64(320), "Height": float64(240), "AspectRatio": "4:3", "PixelFormat": "yuv444p",
				"CodecTimeBase": "1/50", "TimeBase": "1/1000", "NalLengthSize": "4", "BitDepth": float64(8),
				"RefFrames": float64(1), "Level": float64(13), "AverageFrameRate": float64(25), "RealFrameRate": float64(25),
				"IsAVC": true, "IsAnamorphic": false, "IsInterlaced": false, "IsDefault": true,
				"DvVersionMajor": float64(1), "DvVersionMinor": float64(0), "DvProfile": float64(5), "DvLevel": float64(6),
				"RpuPresentFlag": float64(1), "ElPresentFlag": float64(0), "BlPresentFlag": float64(1), "DvBlSignalCompatibilityId": float64(0), "Rotation": float64(-180),
			},
			stream1: map[string]any{"Codec": "eac3", "Index": float64(1), "Channels": float64(6), "ChannelLayout": "5.1", "SampleRate": float64(48000), "BitRate": float64(640000), "Language": "eng", "IsDefault": true, "IsOriginal": true},
		},
		{
			name:    "MP4 with anamorphic video and normalized subtitle codecs",
			fixture: "video_mp4_metadata.json", streams: 6,
			stream0: map[string]any{
				"Codec": "h264", "Profile": "High", "Index": float64(0),
				"Width": float64(720), "Height": float64(358), "AspectRatio": "2.40:1", "PixelFormat": "yuv420p", "Level": float64(31),
				"ColorRange": "tv", "ColorSpace": "smpte170m", "ColorTransfer": "bt709", "ColorPrimaries": "smpte170m",
				"RefFrames": float64(1), "IsAVC": true, "IsAnamorphic": true, "BitRate": float64(1147365), "BitDepth": float64(8), "Language": "und",
			},
			stream1: map[string]any{"Codec": "aac", "Index": float64(1), "Channels": float64(7), "SampleRate": float64(48000), "BitRate": float64(224197), "Language": "eng", "IsDefault": true},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			info := normalizeFixture(t, tc.fixture)
			if tc.checkContainer && info.Container != tc.container {
				t.Fatalf("container = %q, want %q", info.Container, tc.container)
			}
			if got := len(info.MediaStreams); got != tc.streams {
				t.Fatalf("stream count = %d, want %d", got, tc.streams)
			}
			if info.MediaStreams[0].Type != playback.StreamTypeVideo || info.MediaStreams[1].Type != playback.StreamTypeAudio {
				t.Fatalf("first two stream types = %v, %v; want video, audio", info.MediaStreams[0].Type, info.MediaStreams[1].Type)
			}
			assertJSONFields(t, info.MediaStreams[0], tc.stream0)
			assertJSONFields(t, info.MediaStreams[1], tc.stream1)
			if tc.fixture == "video_metadata.json" && info.VideoStream() != &info.MediaStreams[0] {
				t.Fatal("VideoStream did not select normalized video stream")
			}
			if tc.fixture == "video_mp4_metadata.json" {
				if info.MediaStreams[3].Type != playback.StreamTypeSubtitle || info.MediaStreams[4].Type != playback.StreamTypeSubtitle {
					t.Fatalf("MP4 subtitle types = %v, %v; want subtitle", info.MediaStreams[3].Type, info.MediaStreams[4].Type)
				}
				assertJSONFields(t, info.MediaStreams[3], map[string]any{"Codec": "DVDSUB", "Language": "spa"})
				assertJSONFields(t, info.MediaStreams[4], map[string]any{"Codec": "mov_text", "Language": "eng", "IsHearingImpaired": true})
			}
		})
	}
}

func TestNormalizeMatchesCompleteSourceMediaInfoGoldens(t *testing.T) {
	// These complete expected MediaInfo payloads were emitted by the digest-pinned
	// stock Jellyfin normalizer for the eight copied upstream ffprobe inputs.
	for _, fixture := range []string{
		"video_metadata.json", "video_mp4_metadata.json", "video_ts.json", "video_webm.json",
		"video_web_like_mkv_with_subtitle.json", "video_missing_video_bitrate.json",
		"video_nanosecond_duration_bitrate.json", "video_missing_video_bitrate_unknown_audio.json",
	} {
		t.Run(fixture, func(t *testing.T) {
			info := normalizeFixture(t, fixture)
			expected, err := os.ReadFile(filepath.Join("testdata", "upstream-b04614e", "expected-media-info", fixture))
			if err != nil {
				t.Fatal(err)
			}
			var want any
			if err := json.Unmarshal(expected, &want); err != nil {
				t.Fatal(err)
			}
			assertSharedSourceGolden(t, info, want, "MediaInfo")
		})
	}
}

func assertSharedSourceGolden(t *testing.T, actual, expected any, path string) {
	t.Helper()
	encoded, err := json.Marshal(actual)
	if err != nil {
		t.Fatal(err)
	}
	var got any
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	assertSourceGoldenValue(t, got, expected, path)
}

func assertSourceGoldenValue(t *testing.T, actual, expected any, path string) {
	t.Helper()
	switch got := actual.(type) {
	case map[string]any:
		want, ok := expected.(map[string]any)
		if !ok {
			t.Errorf("%s = %#v, want object", path, expected)
			return
		}
		for key, gotValue := range got {
			fieldPath := path + "." + key
			if sourceGoldenExclusion(fieldPath) {
				continue
			}
			wantValue, present := want[key]
			if !present {
				t.Errorf("%s = %#v has no stock golden field", fieldPath, gotValue)
				continue
			}
			assertSourceGoldenValue(t, gotValue, wantValue, fieldPath)
		}
		for key, wantValue := range want {
			fieldPath := path + "." + key
			if sourceGoldenExclusion(fieldPath) || !populatedSourceGoldenValue(wantValue) {
				continue
			}
			if _, present := got[key]; !present {
				t.Errorf("%s is absent, want populated stock golden field %#v", fieldPath, wantValue)
			}
		}
	case []any:
		want, ok := expected.([]any)
		if !ok || len(got) != len(want) {
			t.Errorf("%s = %#v, want %d entries", path, actual, len(want))
			return
		}
		for index := range want {
			assertSourceGoldenValue(t, got[index], want[index], fmt.Sprintf("%s[%d]", path, index))
		}
	default:
		if !reflect.DeepEqual(actual, expected) {
			t.Errorf("%s = %#v, want %#v", path, actual, expected)
		}
	}
}

func sourceGoldenExclusion(path string) bool {
	// DisplayTitle is P5 polish. The committed C# harness also injects
	// ShowName="show" although the eight raw ffprobe inputs contain no show tag;
	// that separately tracked oracle-provenance contradiction is not synthesized
	// by production. P8 completed the formerly excluded tag-mining fields.
	if strings.HasSuffix(path, ".DisplayTitle") {
		return true
	}
	switch path {
	case "MediaInfo.ShowName":
		return true
	default:
		return false
	}
}

func populatedSourceGoldenValue(value any) bool {
	// JSON null is the only absent source fact. Empty strings and collections
	// are observable Jellyfin semantics and must not silently become nil.
	return value != nil
}

func TestNormalizeFormatAndBitrateFallbacksFromPinnedFixtures(t *testing.T) {
	cases := []struct {
		fixture            string
		container          string
		videoBitrate       any
		assertVideoBitrate bool
		bitrate            any
	}{
		{"video_webm.json", "mkv,webm", nil, false, nil},
		{"video_web_like_mkv_with_subtitle.json", "mkv", nil, false, nil},
		{"video_missing_video_bitrate.json", "mov,mp4,m4a,3gp,3g2,mj2", float64(5000000), true, float64(5128000)},
		{"video_nanosecond_duration_bitrate.json", "mkv", float64(800000), true, nil},
		{"video_missing_video_bitrate_unknown_audio.json", "mov,mp4,m4a,3gp,3g2,mj2", nil, true, float64(5128000)},
	}
	for _, tc := range cases {
		t.Run(tc.fixture, func(t *testing.T) {
			info := normalizeFixture(t, tc.fixture)
			if got := info.Container; got != tc.container {
				t.Fatalf("container = %q, want %q", got, tc.container)
			}
			stream := info.VideoStream()
			if stream == nil {
				t.Fatal("missing video stream")
			}
			if tc.assertVideoBitrate {
				if tc.videoBitrate == nil {
					if stream.BitRate != nil {
						t.Fatalf("BitRate = %v, want nil", *stream.BitRate)
					}
				} else {
					assertJSONFields(t, *stream, map[string]any{"BitRate": tc.videoBitrate})
				}
			}
			if tc.bitrate != nil {
				assertJSONFields(t, info.MediaSourceInfo, map[string]any{"Bitrate": tc.bitrate})
			}
		})
	}
}

func TestNormalizeUsesDotNetInt32RoundingForTagDerivedBitrate(t *testing.T) {
	raw := []byte(`{"streams":[{"index":0,"codec_type":"video","codec_name":"h264","width":1,"height":1,"tags":{"NUMBER_OF_BYTES":"5","DURATION":"00:00:16.000000000"}}],"format":{"format_name":"matroska"}}`)
	info, err := probe.Normalize(context.Background(), raw, probe.Options{Path: "/media/rounding.mkv", Protocol: "File", VideoType: "VideoFile"})
	if err != nil {
		t.Fatal(err)
	}
	if got := info.MediaStreams[0].BitRate; got == nil || *got != 2 {
		t.Fatalf("tag-derived BitRate = %v, want 2 (Convert.ToInt32(2.5) midpoint-to-even)", got)
	}
}

func TestNormalizeTruncatesTagDurationToDotNetTimeSpanPrecision(t *testing.T) {
	raw := []byte(`{"streams":[{"index":0,"codec_type":"video","codec_name":"h264","width":1,"height":1,"tags":{"NUMBER_OF_BYTES":"15","DURATION":"00:00:16.000000090"}}],"format":{"format_name":"matroska"}}`)
	info, err := probe.Normalize(context.Background(), raw, probe.Options{Path: "/media/timespan.mkv", Protocol: "File", VideoType: "VideoFile"})
	if err != nil {
		t.Fatal(err)
	}
	if got := info.MediaStreams[0].BitRate; got == nil || *got != 8 {
		t.Fatalf("tag-derived BitRate = %v, want 8 after .NET TimeSpan seven-digit fractional truncation and Convert.ToInt32 midpoint-to-even", got)
	}
}

func TestNormalizeRoundsChapterMillisecondsLikeDotNet(t *testing.T) {
	raw := []byte(`{"streams":[],"format":{"format_name":"matroska"},"chapters":[{"start_time":"0.0005","tags":{"title":"zero"}},{"start_time":"0.0015","tags":{"title":"two"}},{"start_time":"-0.00050001","tags":{"title":"negative zero"}}]}`)
	info, err := probe.Normalize(context.Background(), raw, probe.Options{Path: "/media/chapters.mkv", Protocol: "File", VideoType: "VideoFile"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(info.Chapters), 3; got != want {
		t.Fatalf("chapter count = %d, want %d", got, want)
	}
	if got := info.Chapters[0].StartPositionTicks; got != 0 {
		t.Fatalf("0.0005 chapter ticks = %d, want 0 (Math.Round midpoint-to-even milliseconds)", got)
	}
	if got := info.Chapters[1].StartPositionTicks; got != 20_000 {
		t.Fatalf("0.0015 chapter ticks = %d, want 20000", got)
	}
	if got := info.Chapters[2].StartPositionTicks; got != 0 {
		t.Fatalf("-0.00050001 chapter ticks = %d, want 0 after TimeSpan tick truncation then midpoint-to-even milliseconds", got)
	}
}

func TestNormalizeRejectsNonFiniteAndOutOfRangeChapterStartTimes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		start   string
		wantErr bool
	}{
		{name: "NaN", start: "NaN", wantErr: true},
		{name: "out of TimeSpan range", start: "1e20", wantErr: true},
		{name: "unparseable remains zero", start: "not-a-number"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := []byte(`{"streams":[],"format":{"format_name":"matroska"},"chapters":[{"start_time":"` + tc.start + `"}]}`)
			info, err := probe.Normalize(context.Background(), raw, probe.Options{Path: "/media/chapters.mkv", Protocol: "File", VideoType: "VideoFile"})
			if tc.wantErr {
				if err == nil {
					t.Fatal("Normalize accepted invalid chapter start_time")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := info.Chapters[0].StartPositionTicks; got != 0 {
				t.Fatalf("unparseable chapter ticks = %d, want 0", got)
			}
		})
	}
}

func TestNormalizeRejectsOutOfRangeTagDerivedBitrate(t *testing.T) {
	raw := []byte(`{"streams":[{"index":0,"codec_type":"video","codec_name":"h264","width":1,"height":1,"tags":{"NUMBER_OF_BYTES":"268435456","DURATION":"00:00:01.000000000"}}],"format":{"format_name":"matroska"}}`)
	_, err := probe.Normalize(context.Background(), raw, probe.Options{Path: "/media/oversized.mkv", Protocol: "File", VideoType: "VideoFile"})
	if err == nil {
		t.Fatal("Normalize accepted a tag-derived bitrate beyond Int32.MaxValue")
	}
	if !strings.Contains(err.Error(), "Int32") {
		t.Fatalf("error = %v, want Int32 conversion failure", err)
	}
}

func TestNormalizeRejectsFractionalAndOutOfRangeWireIntegers(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stream string
	}{
		{name: "fractional level", stream: `"level":3.5`},
		{name: "out of range level", stream: `"level":2147483648`},
		{name: "out of range index", stream: `"index":2147483648`},
		{name: "out of range channels", stream: `"channels":2147483648`},
		{name: "out of range bits numeric", stream: `"bits_per_raw_sample":2147483648`},
		{name: "out of range bits quoted", stream: `"bits_per_raw_sample":"2147483648"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := []byte(`{"streams":[{"codec_type":"video","codec_name":"h264",` + tc.stream + `}],"format":{"format_name":"matroska"}}`)
			if _, err := probe.Normalize(context.Background(), raw, probe.Options{Path: "/media/scalar.mkv", Protocol: "File", VideoType: "VideoFile"}); err == nil {
				t.Fatal("Normalize accepted a fractional or Int32-overflow wire scalar")
			}
		})
	}
}

func TestNormalizeClampsTimeSpanChapterEndpointsLikeDotNet(t *testing.T) {
	for _, tc := range []struct {
		seconds string
		want    int64
	}{
		{seconds: "922337203685.4776", want: 9_223_372_036_854_770_000},
		{seconds: "-922337203685.4776", want: -9_223_372_036_854_770_000},
	} {
		t.Run(tc.seconds, func(t *testing.T) {
			raw := []byte(`{"streams":[],"format":{"format_name":"matroska"},"chapters":[{"start_time":"` + tc.seconds + `"}]}`)
			info, err := probe.Normalize(context.Background(), raw, probe.Options{Path: "/media/chapter-endpoint.mkv", Protocol: "File", VideoType: "VideoFile"})
			if err != nil {
				t.Fatal(err)
			}
			if got := info.Chapters[0].StartPositionTicks; got != tc.want {
				t.Fatalf("chapter ticks = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestNormalizeUsesFirstNonBlankMetadataTag(t *testing.T) {
	raw := []byte(`{"streams":[],"format":{"format_name":"matroska","tags":{"title":"   ","title-eng":"Actual"}}}`)
	info, err := probe.Normalize(context.Background(), raw, probe.Options{Path: "/media/title.mkv", Protocol: "File", VideoType: "VideoFile"})
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Name; got == nil || *got != "Actual" {
		t.Fatalf("Name = %v, want first nonblank title-eng value %q", got, "Actual")
	}
}

// ProbeResultNormalizer.cs maps ShowName only when the raw show tag exists.
func TestNormalizeMapsShowNameOnlyFromSourceShowTag(t *testing.T) {
	raw := []byte(`{"streams":[],"format":{"format_name":"matroska","tags":{"show":"Source show"}}}`)
	info, err := probe.Normalize(context.Background(), raw, probe.Options{Path: "/media/show.mkv", Protocol: "File", VideoType: "VideoFile"})
	if err != nil {
		t.Fatal(err)
	}
	if got := info.ShowName; got == nil || *got != "Source show" {
		t.Fatalf("ShowName = %v, want source show tag value", got)
	}
}

func TestNormalizeAllowsNullFormatTagToClearStreamTitle(t *testing.T) {
	raw := []byte(`{"streams":[{"index":0,"codec_type":"video","codec_name":"h264","width":1,"height":1,"tags":{"title":"Stream title"}}],"format":{"format_name":"matroska","tags":{"TITLE":null}}}`)
	info, err := probe.Normalize(context.Background(), raw, probe.Options{Path: "/media/cleared-title.mkv", Protocol: "File", VideoType: "VideoFile"})
	if err != nil {
		t.Fatal(err)
	}
	if info.Name != nil {
		t.Fatalf("Name = %q, want nil after case-insensitive null format tag", *info.Name)
	}
}

// C# MediaInfo constructor distinction: source-null strings are omitted on the
// JSON wire, while collections allocated by the constructor serialize as [] or {}.
func TestNormalizePreservesNullableStringsAndConstructedCollectionsOnWire(t *testing.T) {
	raw := []byte(`{"streams":[],"format":{"format_name":"matroska"}}`)
	info, err := probe.Normalize(context.Background(), raw, probe.Options{Path: "/media/empty.mkv", Protocol: "File", VideoType: "VideoFile"})
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []struct {
		name  string
		value *string
	}{
		{"Album", info.Album},
		{"Name", info.Name},
		{"ShowName", info.ShowName},
		{"ForcedSortName", info.ForcedSortName},
		{"OfficialRating", info.OfficialRating},
		{"OfficialRatingDescription", info.OfficialRatingDescription},
		{"Overview", info.Overview},
	} {
		if field.value != nil {
			t.Errorf("%s = %q, want source null", field.name, *field.value)
		}
	}
	if info.Chapters == nil || info.Artists == nil || info.AlbumArtists == nil || info.Studios == nil || info.Genres == nil || info.People == nil || info.ProviderIDs == nil || info.MediaStreams == nil || info.MediaAttachments == nil || info.Formats == nil || info.RequiredHTTPHeaders == nil {
		t.Fatal("Normalize did not preserve MediaInfo constructor-initialized collections/maps")
	}
	encoded, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Album", "Name", "ShowName", "ForcedSortName", "OfficialRating", "OfficialRatingDescription", "Overview"} {
		if got, exists := fields[name]; exists {
			t.Errorf("wire %s = %s (present %t), want omitted source null", name, got, exists)
		}
	}
	for name, want := range map[string]string{
		"Chapters": "[]", "Artists": "[]", "AlbumArtists": "[]", "Studios": "[]", "Genres": "[]",
		"People": "[]", "ProviderIds": "{}",
		"MediaStreams": "[]", "MediaAttachments": "[]", "Formats": "[]", "RequiredHttpHeaders": "{}",
	} {
		if got, exists := fields[name]; !exists || string(got) != want {
			t.Errorf("wire %s = %s (present %t), want %s", name, got, exists, want)
		}
	}
}

// GetChapterInfo leaves Name unset when the pinned source chapter has no title
// tag (ProbeResultNormalizer.cs:1598-1616); it must not become an empty string.
func TestNormalizePreservesTitlelessChapterNameAsAbsentOnWire(t *testing.T) {
	raw := []byte(`{"streams":[],"chapters":[{"start_time":"0"}],"format":{"format_name":"matroska"}}`)
	info, err := probe.Normalize(context.Background(), raw, probe.Options{Path: "/media/titleless-chapter.mkv", Protocol: "File", VideoType: "VideoFile"})
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Chapters[0].Name; got != nil {
		t.Fatalf("titleless chapter Name = %q, want source null", *got)
	}
	encoded, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	var fields struct {
		Chapters []map[string]json.RawMessage
	}
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields.Chapters) != 1 {
		t.Fatalf("wire Chapters = %#v, want one chapter", fields.Chapters)
	}
	if got, present := fields.Chapters[0]["Name"]; present && string(got) != "null" {
		t.Fatalf("titleless wire chapter Name = %s, want absent or null source value", got)
	}
}

func TestNormalizeDerivesProductionYearFromOrderedDateTags(t *testing.T) {
	for _, tc := range []struct {
		name     string
		tags     string
		want     int
		premiere string
	}{
		{name: "date", tags: `"date":"2024-05-06"`, want: 2024, premiere: "2024-05-06"},
		{name: "originaldate fallback", tags: `"originaldate":"2024-05-06"`, want: 2024, premiere: "2024-05-06"},
		{name: "numeric date precedes originaldate", tags: `"date":"2023","originaldate":"2024-05-06"`, want: 2023},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := []byte(`{"streams":[],"format":{"format_name":"matroska","tags":{` + tc.tags + `}}}`)
			info, err := probe.Normalize(context.Background(), raw, probe.Options{Path: "/media/date.mkv", Protocol: "File", VideoType: "VideoFile"})
			if err != nil {
				t.Fatal(err)
			}
			if got := info.ProductionYear; got == nil || *got != tc.want {
				t.Fatalf("ProductionYear = %v, want %d", got, tc.want)
			}
			if tc.premiere != "" {
				if got := info.PremiereDate; got == nil || got.Format("2006-01-02") != tc.premiere {
					t.Fatalf("PremiereDate = %v, want date %s", got, tc.premiere)
				}
			}
		})
	}
}

func TestNormalizeUsesOrderedAndWhitespaceTolerantMetadataIndices(t *testing.T) {
	for _, tc := range []struct {
		name string
		tags string
		want int
	}{
		{name: "episode sort precedes episode ID", tags: `"episode_sort":" 7 ","episode_id":"8"`, want: 7},
		{name: "episode ID fallback", tags: `"episode_id":" 8 "`, want: 8},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := []byte(`{"streams":[],"format":{"format_name":"matroska","tags":{` + tc.tags + `}}}`)
			info, err := probe.Normalize(context.Background(), raw, probe.Options{Path: "/media/episode.mkv", Protocol: "File", VideoType: "VideoFile"})
			if err != nil {
				t.Fatal(err)
			}
			if got := info.IndexNumber; got == nil || *got != tc.want {
				t.Fatalf("IndexNumber = %v, want %d", got, tc.want)
			}
		})
	}
}

func TestNormalizePreservesZeroAndNegativeSampleRates(t *testing.T) {
	for _, rate := range []string{"0", "-1"} {
		t.Run(rate, func(t *testing.T) {
			raw := []byte(`{"streams":[{"index":0,"codec_type":"audio","codec_name":"aac","sample_rate":"` + rate + `"}],"format":{"format_name":"matroska"}}`)
			info, err := probe.Normalize(context.Background(), raw, probe.Options{Path: "/media/rate.mkv", Protocol: "File", VideoType: "VideoFile"})
			if err != nil {
				t.Fatal(err)
			}
			if got := info.MediaStreams[0].SampleRate; got == nil || *got != map[string]int{"0": 0, "-1": -1}[rate] {
				t.Fatalf("SampleRate = %v, want %s", got, rate)
			}
		})
	}
}

func TestNormalizeUsesPinnedGenreDelimiterRules(t *testing.T) {
	for _, tc := range []struct {
		genre string
		want  []string
	}{
		{genre: "Rock|Pop", want: []string{"Rock", "Pop"}},
		{genre: "Rock, Pop/Other", want: []string{"Rock, Pop", "Other"}},
	} {
		t.Run(tc.genre, func(t *testing.T) {
			raw := []byte(`{"streams":[],"format":{"format_name":"matroska","tags":{"genre":"` + tc.genre + `"}}}`)
			info, err := probe.Normalize(context.Background(), raw, probe.Options{Path: "/media/genre.mkv", Protocol: "File", VideoType: "VideoFile"})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(info.Genres, tc.want) {
				t.Fatalf("Genres = %#v, want %#v", info.Genres, tc.want)
			}
		})
	}
}

func TestNormalizeMapsCommonArtistTags(t *testing.T) {
	for _, tc := range []struct {
		name string
		tags string
		want []string
	}{
		{name: "plural", tags: `"artists":"A/B"`, want: []string{"A", "B"}},
		{name: "singular fallback", tags: `"artist":"A feat. B"`, want: []string{"A", "B"}},
		{name: "plural comma stays one", tags: `"artists":"Earth, Wind \u0026 Fire"`, want: []string{"Earth, Wind & Fire"}},
		{name: "singular slash whitelist", tags: `"artist":"AC/DC"`, want: []string{"AC/DC"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := []byte(`{"streams":[],"format":{"format_name":"matroska","tags":{` + tc.tags + `}}}`)
			info, err := probe.Normalize(context.Background(), raw, probe.Options{Path: "/media/artists.mkv", Protocol: "File", VideoType: "VideoFile"})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(info.Artists, tc.want) {
				t.Fatalf("Artists = %#v, want %#v", info.Artists, tc.want)
			}
		})
	}
}

func TestNormalizeMinesPinnedAudioTagsInStockOrder(t *testing.T) {
	info := normalizeTagFixture(t, true, nil, map[string]string{
		"title":                        "Track",
		"artist":                       "Track Artist",
		"albumartist":                  "Album Artist",
		"composer":                     "Composer One; Composer Two",
		"conductor":                    "Conductor",
		"lyricist":                     "Lyricist",
		"performer":                    "Performer (lead GUITAR); Élodie (éLECTRIC VIOLIN); Band Without Role",
		"writer":                       "Writer",
		"arranger":                     "Arranger",
		"engineer":                     "Engineer",
		"mixer":                        "Mixer",
		"remixer":                      "Remixer",
		"organization":                 "Earlier Studio",
		"ensemble":                     "Another Studio",
		"publisher":                    "Publisher",
		"label":                        "Album Artist, Final Label, final label",
		"track":                        "7/12",
		"disc":                         "2/2",
		"tracknumber":                  "99",
		"MusicBrainz Album Artist Id":  "album-artist-id/ignored",
		"MUSICBRAINZ_ARTISTID":         "artist-id",
		"MusicBrainz Album Id":         "album-id",
		"MUSICBRAINZ_RELEASEGROUPID":   "group-id/ignored-group",
		"MusicBrainz Release Track Id": "track-id",
	})

	if got, want := info.AlbumArtists, []string{"Album Artist"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("AlbumArtists = %#v, want %#v", got, want)
	}
	if got, want := info.Studios, []string{"Final Label"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Studios = %#v, want last non-empty tag %#v", got, want)
	}
	if info.IndexNumber == nil || *info.IndexNumber != 7 || info.ParentIndexNumber == nil || *info.ParentIndexNumber != 2 {
		t.Fatalf("track/disc = %v/%v, want 7/2", info.IndexNumber, info.ParentIndexNumber)
	}
	wantPeople := []probe.MediaPerson{
		{Name: "Composer One", Type: "Composer"},
		{Name: "Composer Two", Type: "Composer"},
		{Name: "Conductor", Type: "Conductor"},
		{Name: "Lyricist", Type: "Lyricist"},
		{Name: "Performer", Type: "Actor", Role: stringPointer("Lead GUITAR")},
		{Name: "Élodie", Type: "Actor", Role: stringPointer("Électric VIOLIN")},
		{Name: "Writer", Type: "Writer"},
		{Name: "Arranger", Type: "Arranger"},
		{Name: "Engineer", Type: "Engineer"},
		{Name: "Mixer", Type: "Mixer"},
		{Name: "Remixer", Type: "Remixer"},
	}
	if !reflect.DeepEqual(info.People, wantPeople) {
		t.Fatalf("People = %#v, want %#v", info.People, wantPeople)
	}
	wantIDs := map[string]string{
		"MusicBrainzAlbumArtist":  "album-artist-id",
		"MusicBrainzArtist":       "artist-id",
		"MusicBrainzAlbum":        "album-id",
		"MusicBrainzReleaseGroup": "group-id",
		"MusicBrainzTrack":        "track-id",
	}
	if !reflect.DeepEqual(info.ProviderIDs, wantIDs) {
		t.Fatalf("ProviderIDs = %#v, want %#v", info.ProviderIDs, wantIDs)
	}

	albumArtists := normalizeTagFixture(t, true, nil, map[string]string{
		"artist":       "Fallback Artist",
		"albumartists": "Ignored Plural Artist",
		"albumartist":  "First Artist, Second Artist",
	})
	if got, want := albumArtists.AlbumArtists, []string{"First Artist, Second Artist"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("pinned AlbumArtists = %#v, want %#v", got, want)
	}
}

func TestNormalizeMinesPinnedITunesTagsWithStockOverwriteSemantics(t *testing.T) {
	plist := `<plist version="1.0"><dict>` +
		`<key>studio</key><string>iTunes Studio</string>` +
		`<key>screenwriters</key><array><dict><key>name</key><string>Writer One</string></dict><dict><key>name</key><string>writer one</string></dict></array>` +
		`<key>directors</key><array><dict><key>name</key><string>Ignored Final Director</string></dict></array>` +
		`</dict></plist>`
	info := normalizeTagFixture(t, false, nil, map[string]string{
		"copyright": "Copyright Studio",
		"iTunEXTC":  "mpaa|G|100|For crude humor",
		"iTunMOVI":  plist,
	})

	if got, want := info.Studios, []string{"iTunes Studio"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Studios = %#v, want %#v", got, want)
	}
	if got, want := info.People, []probe.MediaPerson{{Name: "Writer One", Type: "Writer"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("People = %#v, want final processed (not final XML) key %#v", got, want)
	}
	if info.OfficialRating == nil || *info.OfficialRating != "G" {
		t.Fatalf("OfficialRating = %v, want G", info.OfficialRating)
	}
	if info.OfficialRatingDescription == nil || *info.OfficialRatingDescription != "For crude humor" {
		t.Fatalf("OfficialRatingDescription = %v", info.OfficialRatingDescription)
	}

	emptyField := normalizeTagFixture(t, false, nil, map[string]string{"iTunEXTC": "mpaa||G|100|Shifted description"})
	if emptyField.OfficialRating == nil || *emptyField.OfficialRating != "G" || emptyField.OfficialRatingDescription == nil || *emptyField.OfficialRatingDescription != "Shifted description" {
		t.Fatalf("pinned iTunEXTC empty-field removal = %v/%v", emptyField.OfficialRating, emptyField.OfficialRatingDescription)
	}

	malformed := normalizeTagFixture(t, false, nil, map[string]string{"iTunMOVI": "<"})
	if malformed.People == nil || malformed.Studios == nil {
		t.Fatal("malformed iTunMOVI changed constructed collection semantics")
	}
}

func TestNormalizeMinesWTVTagsOnlyFromFormat(t *testing.T) {
	streamOnly := normalizeTagFixture(t, false, map[string]string{
		"WM/MediaCredits": "Stream Actor",
		"WM/Genre":        "Stream Genre",
	}, nil)
	if len(streamOnly.People) != 0 || len(streamOnly.Genres) != 0 {
		t.Fatalf("stream-only WTV tags leaked into format mapping: People=%#v Genres=%#v", streamOnly.People, streamOnly.Genres)
	}

	info := normalizeTagFixture(t, false, nil, map[string]string{
		"WM/Genre":                          "Drama/Science Fiction,Comedy",
		"WM/ParentalRating":                 "TV-14",
		"WM/MediaCredits":                   "Actor One/Actor Two",
		"WM/OriginalReleaseTime":            "2020",
		"WM/MediaOriginalBroadcastDateTime": "source checks presence but parses release year",
		"WM/SubTitleDescription":            "4/13. The Episode: The long description is discarded by stock extraction",
	})
	if got, want := info.Genres, []string{"Drama", "Science Fiction", "Comedy"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Genres = %#v, want %#v", got, want)
	}
	if got, want := info.People, []probe.MediaPerson{{Name: "Actor One", Type: "Actor"}, {Name: "Actor Two", Type: "Actor"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("People = %#v, want %#v", got, want)
	}
	if info.OfficialRating == nil || *info.OfficialRating != "TV-14" {
		t.Fatalf("OfficialRating = %v, want TV-14", info.OfficialRating)
	}
	if info.ProductionYear == nil || *info.ProductionYear != 2020 || info.PremiereDate == nil || info.PremiereDate.Format("2006-01-02") != "2020-01-01" {
		t.Fatalf("production/premiere = %v/%v, want 2020/2020-01-01", info.ProductionYear, info.PremiereDate)
	}
	if info.IndexNumber == nil || *info.IndexNumber != 4 || info.Overview == nil || *info.Overview != "The Episode" {
		t.Fatalf("episode/overview = %v/%v, want 4/The Episode", info.IndexNumber, info.Overview)
	}

	withoutBroadcastTag := normalizeTagFixture(t, false, nil, map[string]string{"WM/OriginalReleaseTime": "2020"})
	if withoutBroadcastTag.PremiereDate != nil {
		t.Fatalf("PremiereDate = %v without WM/MediaOriginalBroadcastDateTime tag", withoutBroadcastTag.PremiereDate)
	}

	emptyCredits := normalizeTagFixture(t, false, nil, map[string]string{"WM/MediaCredits": " / ; / "})
	if emptyCredits.People == nil || len(emptyCredits.People) != 0 {
		t.Fatalf("separator-only WTV People = %#v, want non-nil empty slice", emptyCredits.People)
	}
}

func normalizeTagFixture(t *testing.T, isAudio bool, streamTags, formatTags map[string]string) probe.MediaInfo {
	t.Helper()
	streamKind := "video"
	if isAudio {
		streamKind = "audio"
	}
	raw, err := json.Marshal(map[string]any{
		"streams": []any{map[string]any{
			"index":      0,
			"codec_type": streamKind,
			"codec_name": "test",
			"width":      1,
			"height":     1,
			"tags":       streamTags,
		}},
		"format": map[string]any{
			"format_name": "matroska",
			"tags":        formatTags,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	info, err := probe.Normalize(context.Background(), raw, probe.Options{
		IsAudio:   isAudio,
		Path:      "/media/tagged",
		Protocol:  "File",
		VideoType: "VideoFile",
	})
	if err != nil {
		t.Fatal(err)
	}
	return info
}

func TestNormalizeDoesNotTakePathFromProbeMetadata(t *testing.T) {
	raw := []byte(`{"streams":[],"format":{"format_name":"matroska","tags":{"filename":"/spoofed"}}}`)
	info, err := probe.Normalize(context.Background(), raw, probe.Options{Protocol: "File", VideoType: "VideoFile"})
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Path; got != "" {
		t.Fatalf("Path = %q, want caller-owned empty path", got)
	}
}

func TestNormalizeDropsOutOfRangeTryParseStringScalars(t *testing.T) {
	const overInt32 = "2147483648"
	for _, tc := range []struct {
		name   string
		stream string
		format string
		assert func(*testing.T, *probe.MediaInfo)
	}{
		{name: "stream bitrate", stream: `"bit_rate":"` + overInt32 + `"`, assert: func(t *testing.T, info *probe.MediaInfo) {
			if info.MediaStreams[0].BitRate != nil {
				t.Fatalf("BitRate = %d, want nil", *info.MediaStreams[0].BitRate)
			}
		}},
		{name: "sample rate", stream: `"sample_rate":"` + overInt32 + `"`, assert: func(t *testing.T, info *probe.MediaInfo) {
			if info.MediaStreams[0].SampleRate != nil {
				t.Fatalf("SampleRate = %d, want nil", *info.MediaStreams[0].SampleRate)
			}
		}},
		{name: "tag bitrate", stream: `"tags":{"BPS":"` + overInt32 + `"}`, assert: func(t *testing.T, info *probe.MediaInfo) {
			if info.MediaStreams[0].BitRate != nil {
				t.Fatalf("BitRate = %d, want nil", *info.MediaStreams[0].BitRate)
			}
		}},
		{name: "format bitrate", format: `,"bit_rate":"` + overInt32 + `"`, assert: func(t *testing.T, info *probe.MediaInfo) {
			if info.Bitrate != nil {
				t.Fatalf("Bitrate = %d, want nil", *info.Bitrate)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := []byte(`{"streams":[{"index":0,"codec_type":"video","codec_name":"h264","width":1920,"height":1080` + optionalJSONMember(tc.stream) + `}],"format":{"format_name":"matroska"` + tc.format + `}}`)
			info, err := probe.Normalize(context.Background(), raw, probe.Options{Path: "/media/scalar.mkv", Protocol: "File", VideoType: "VideoFile"})
			if err != nil {
				t.Fatal(err)
			}
			tc.assert(t, &info)
		})
	}
}

func optionalJSONMember(member string) string {
	if member == "" {
		return ""
	}
	return "," + member
}

func TestNormalizeRejectsNonStringTagValues(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{name: "stream", raw: `{"streams":[{"index":0,"codec_type":"video","codec_name":"h264","tags":{"title":123}}],"format":{"format_name":"matroska"}}`},
		{name: "format", raw: `{"streams":[],"format":{"format_name":"matroska","tags":{"title":123}}}`},
		{name: "chapter", raw: `{"streams":[],"format":{"format_name":"matroska"},"chapters":[{"start_time":"0","tags":{"title":123}}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := probe.Normalize(context.Background(), []byte(tc.raw), probe.Options{Path: "/media/tags.mkv", Protocol: "File", VideoType: "VideoFile"}); err == nil {
				t.Fatal("Normalize accepted a non-string ffprobe tag value")
			}
		})
	}
}

func TestNormalizeAttachmentFromPinnedNormalizerMapping(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "upstream-b04614e", "attachment.json"))
	if err != nil {
		t.Fatal(err)
	}
	info, err := probe.Normalize(context.Background(), raw, probe.Options{Path: "/media/attachment.mkv", Protocol: "File", VideoType: "VideoFile"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(info.MediaAttachments), 1; got != want {
		t.Fatalf("attachment count = %d, want %d", got, want)
	}
	assertJSONFields(t, info.MediaAttachments[0], map[string]any{
		"Codec": "ttf", "Index": float64(7), "FileName": "Regular.ttf",
		"MimeType": "application/x-truetype-font", "Comment": "Jellyfin b04614e ProbeResultNormalizer.cs:671-687 attachment mapping",
	})
}

// MediaStreamInfo.cs:204 binds the literal ffprobe property codec_tag_string?.
// The normal ffprobe spelling is therefore intentionally ignored by pinned stock.
func TestNormalizeHonorsLiteralQuestionMarkCodecTagBinding(t *testing.T) {
	for _, tc := range []struct {
		name   string
		member string
		want   *string
	}{
		{name: "normal ffprobe key ignored", member: `"codec_tag_string":"avc1"`},
		{name: "literal source key accepted", member: `"codec_tag_string?":"avc1"`, want: stringPointer("avc1")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := []byte(`{"streams":[{"index":0,"codec_type":"video","codec_name":"h264","width":1,"height":1,` + tc.member + `}],"format":{"format_name":"matroska"}}`)
			info, err := probe.Normalize(context.Background(), raw, probe.Options{Path: "/media/codec-tag.mkv", Protocol: "File", VideoType: "VideoFile"})
			if err != nil {
				t.Fatal(err)
			}
			if got := info.MediaStreams[0].CodecTag; !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("CodecTag = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestNormalizeRejectsAspectFallbackWithoutBothDimensions(t *testing.T) {
	for _, tc := range []struct {
		name       string
		aspect     string
		dimensions string
	}{
		{name: "absent aspect missing width", dimensions: `"height":1080`},
		{name: "absent aspect missing height", dimensions: `"width":1920`},
		{name: "invalid aspect missing width", aspect: `,"display_aspect_ratio":"invalid"`, dimensions: `"height":1080`},
		{name: "invalid aspect missing height", aspect: `,"display_aspect_ratio":"invalid"`, dimensions: `"width":1920`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := []byte(`{"streams":[{"index":0,"codec_type":"video","codec_name":"h264",` + tc.dimensions + tc.aspect + `}],"format":{"format_name":"matroska"}}`)
			if _, err := probe.Normalize(context.Background(), raw, probe.Options{Path: "/media/aspect.mkv", Protocol: "File", VideoType: "VideoFile"}); err == nil {
				t.Fatal("Normalize accepted invalid aspect fallback without both dimensions")
			}
		})
	}
}

func TestNormalizeFallsBackFromInvalidDisplayAspectComponents(t *testing.T) {
	for _, aspect := range []string{"1:0", "-1:1", "2147483648:1"} {
		t.Run(aspect, func(t *testing.T) {
			raw := []byte(`{"streams":[{"index":0,"codec_type":"video","codec_name":"h264","width":1920,"height":1080,"display_aspect_ratio":"` + aspect + `"}],"format":{"format_name":"matroska"}}`)
			info, err := probe.Normalize(context.Background(), raw, probe.Options{Path: "/media/aspect.mkv", Protocol: "File", VideoType: "VideoFile"})
			if err != nil {
				t.Fatal(err)
			}
			if got := info.MediaStreams[0].AspectRatio; got == nil || *got != "16:9" {
				t.Fatalf("AspectRatio = %v, want 16:9", got)
			}
		})
	}
}

func TestNormalizeUsesDoubleSampleAspectRatioSemantics(t *testing.T) {
	raw := []byte(`{"streams":[{"index":0,"codec_type":"video","codec_name":"h264","width":1920,"height":1080,"sample_aspect_ratio":"1.0:1.0"}],"format":{"format_name":"matroska"}}`)
	info, err := probe.Normalize(context.Background(), raw, probe.Options{Path: "/media/decimal-sar.mkv", Protocol: "File", VideoType: "VideoFile"})
	if err != nil {
		t.Fatal(err)
	}
	if got := info.MediaStreams[0].IsAnamorphic; got == nil || *got {
		t.Fatalf("IsAnamorphic = %v, want false for source double-parsed 1.0:1.0 sample aspect ratio", got)
	}
}

func TestNormalizeTreatsNonSquareSampleAspectRatiosAsAnamorphic(t *testing.T) {
	for _, ratio := range []string{"NaN:1", "1:NaN", "Infinity:1", "1:Infinity", "-1:-1"} {
		t.Run(ratio, func(t *testing.T) {
			raw := []byte(`{"streams":[{"index":0,"codec_type":"video","codec_name":"h264","width":1,"height":1,"sample_aspect_ratio":"` + ratio + `"}],"format":{"format_name":"matroska"}}`)
			info, err := probe.Normalize(context.Background(), raw, probe.Options{Path: "/media/nonfinite-sar.mkv", Protocol: "File", VideoType: "VideoFile"})
			if err != nil {
				t.Fatal(err)
			}
			if got := info.MediaStreams[0].IsAnamorphic; got == nil || !*got {
				t.Fatalf("IsAnamorphic = %v, want true for source non-near-square %s", got, ratio)
			}
		})
	}
}

func TestNormalizeRejectsNonIntegerFrameRateComponents(t *testing.T) {
	for _, rate := range []string{"1.5/1", "1e3/2"} {
		t.Run(rate, func(t *testing.T) {
			raw := []byte(`{"streams":[{"index":0,"codec_type":"video","codec_name":"h264","width":1,"height":1,"avg_frame_rate":"` + rate + `"}],"format":{"format_name":"matroska"}}`)
			info, err := probe.Normalize(context.Background(), raw, probe.Options{Path: "/media/rate.mkv", Protocol: "File", VideoType: "VideoFile"})
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(info.MediaStreams[0])
			if err != nil {
				t.Fatal(err)
			}
			var stream map[string]any
			if err := json.Unmarshal(encoded, &stream); err != nil {
				t.Fatal(err)
			}
			if value, present := stream["AverageFrameRate"]; present && value != nil {
				t.Fatalf("AverageFrameRate = %#v, want absent or nil for non-integer rate", value)
			}
		})
	}
}

func TestNormalizeUsesClosedCaseInsensitiveCodecTypeDispatch(t *testing.T) {
	unknown := []byte(`{"streams":[{"index":0,"codec_type":"bogus","codec_name":"h264"}],"format":{"format_name":"matroska"}}`)
	if _, err := probe.Normalize(context.Background(), unknown, probe.Options{Path: "/media/unknown.mkv", Protocol: "File", VideoType: "VideoFile"}); err == nil {
		t.Fatal("Normalize accepted an unknown ffprobe codec_type")
	}
	known := []byte(`{"streams":[{"index":0,"codec_type":"Video","codec_name":"h264","width":1,"height":1,"tags":{"title":"Cased title"}}],"format":{"format_name":"matroska"}}`)
	info, err := probe.Normalize(context.Background(), known, probe.Options{Path: "/media/cased.mkv", Protocol: "File", VideoType: "VideoFile"})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(info.MediaStreams); got != 1 || info.MediaStreams[0].Type != playback.StreamTypeVideo {
		t.Fatalf("codec_type Video normalized streams = %#v, want one video stream", info.MediaStreams)
	}
	if got := info.Name; got == nil || *got != "Cased title" {
		t.Fatalf("Name = %v, want stream title from case-insensitive codec_type", got)
	}
}

func TestNormalizeRejectsUnknownMediaProtocol(t *testing.T) {
	raw := []byte(`{"streams":[],"format":{"format_name":"matroska"}}`)
	for _, protocol := range []string{"Https", "bogus"} {
		t.Run(protocol, func(t *testing.T) {
			if _, err := probe.Normalize(context.Background(), raw, probe.Options{Path: "/media/protocol.mkv", Protocol: protocol, VideoType: "VideoFile"}); err == nil {
				t.Fatal("Normalize accepted an unknown media protocol")
			}
		})
	}
}

func TestNormalizeRejectsUnknownNonemptyVideoType(t *testing.T) {
	raw := []byte(`{"streams":[],"format":{"format_name":"matroska"}}`)
	if _, err := probe.Normalize(context.Background(), raw, probe.Options{Path: "/media/type.mkv", Protocol: "File", VideoType: "tyop"}); err == nil {
		t.Fatal("Normalize accepted an unknown nonempty video type")
	}
}

func TestNormalizeRejectsMalformedJSON(t *testing.T) {
	_, err := probe.Normalize(context.Background(), []byte(`{"streams":[`), probe.Options{Path: "/media/broken.mkv", Protocol: "File", VideoType: "VideoFile"})
	if err == nil {
		t.Fatal("Normalize accepted malformed ffprobe JSON")
	}
}

// Pinned ffprobe model integer fields are nonnullable; JSON null must not be
// silently converted to the Go zero value. Pointer-valued fields are covered
// separately by the source fixture corpus.
func TestNormalizeRejectsNullForNonnullableStreamIntegers(t *testing.T) {
	for _, tc := range []struct {
		name   string
		member string
	}{
		{name: "index", member: `"index":null`},
		{name: "refs", member: `"refs":null`},
		{name: "bits per raw sample", member: `"bits_per_raw_sample":null`},
		{name: "disposition default", member: `"disposition":{"default":null}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := []byte(`{"streams":[{"codec_type":"video","codec_name":"h264","width":1,"height":1,` + tc.member + `}],"format":{"format_name":"matroska"}}`)
			if _, err := probe.Normalize(context.Background(), raw, probe.Options{Path: "/media/null-int.mkv", Protocol: "File", VideoType: "VideoFile"}); err == nil {
				t.Fatal("Normalize accepted JSON null for nonnullable ffprobe integer")
			}
		})
	}
}

func TestNormalizeRejectsNonemptyInvalidFormatScalars(t *testing.T) {
	// ProbeResultNormalizer.cs:239-242 and 1319-1320 parse nonempty format
	// duration and size directly; malformed scalars are errors, not absent facts.
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{name: "size", raw: `{"streams":[],"format":{"format_name":"matroska","size":"not-a-number"}}`},
		{name: "duration", raw: `{"streams":[],"format":{"format_name":"matroska","duration":"not-a-number"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := probe.Normalize(context.Background(), []byte(tc.raw), probe.Options{Path: "/media/invalid.mkv", Protocol: "File", VideoType: "VideoFile"}); err == nil {
				t.Fatal("Normalize accepted nonempty invalid format scalar")
			}
		})
	}
}

func TestNormalizeMatchesTimeSpanDurationBoundary(t *testing.T) {
	// TimeSpan.IntervalFromDoubleTicks maps this rounded boundary to MaxValue;
	// a direct Go float-to-int64 conversion instead produces MinInt64.
	info, err := probe.Normalize(context.Background(), []byte(`{"streams":[],"format":{"format_name":"matroska","duration":"922337203685.4775808"}}`), probe.Options{Path: "/media/boundary.mkv", Protocol: "File", VideoType: "VideoFile"})
	if err != nil {
		t.Fatal(err)
	}
	if info.RunTimeTicks == nil || *info.RunTimeTicks != math.MaxInt64 {
		t.Fatalf("RunTimeTicks = %#v, want TimeSpan.MaxValue %d", info.RunTimeTicks, int64(math.MaxInt64))
	}
}

func TestNormalizeExcludesSubtitleWithBlankCodec(t *testing.T) {
	// ProbeResultNormalizer.cs:115-117 removes whitespace-only subtitle codecs.
	info, err := probe.Normalize(context.Background(), []byte(`{"streams":[{"index":0,"codec_type":"subtitle","codec_name":"   "}],"format":{"format_name":"matroska"}}`), probe.Options{Path: "/media/blank-subtitle.mkv", Protocol: "File", VideoType: "VideoFile"})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(info.MediaStreams); got != 0 {
		t.Fatalf("normalized stream count = %d, want blank-codec subtitle excluded", got)
	}
}

func TestNormalizeAudioUsesStreamDurationBeforeFormatDuration(t *testing.T) {
	// ProbeResultNormalizer.cs:1240-1253 selects stream.Duration, then format.Duration.
	info, err := probe.Normalize(context.Background(), []byte(`{"streams":[{"index":0,"codec_type":"audio","codec_name":"mp3","duration":"2.5"}],"format":{"format_name":"mp3","duration":"1"}}`), probe.Options{IsAudio: true, Path: "/media/audio.mp3", Protocol: "File"})
	if err != nil {
		t.Fatal(err)
	}
	if info.RunTimeTicks == nil || *info.RunTimeTicks != 25_000_000 {
		t.Fatalf("RunTimeTicks = %#v, want stream duration 25000000", info.RunTimeTicks)
	}
}

func TestNormalizePreservesZeroStreamIndicesAndUnparseableChapters(t *testing.T) {
	// GetChapterInfo returns a chapter with its default zero position when the
	// ffprobe start time cannot parse (ProbeResultNormalizer.cs:1607-1616).
	raw := []byte(`{"streams":[{"index":0,"codec_type":"video","codec_name":"h264","width":1920,"height":1080}],"chapters":[{"start_time":"not-a-number","tags":{"title":"Broken"}},{"start_time":"0","tags":{"title":"At zero"}}],"format":{"format_name":"matroska"}}`)
	info, err := probe.Normalize(context.Background(), raw, probe.Options{Path: "/media/chapters.mkv", Protocol: "File", VideoType: "VideoFile"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(info.MediaStreams), 1; got != want || info.MediaStreams[0].Index != 0 {
		t.Fatalf("streams = %#v, want one stream with index zero", info.MediaStreams)
	}
	if got, want := info.Chapters, []probe.Chapter{{Name: stringPointer("Broken"), StartPositionTicks: 0}, {Name: stringPointer("At zero"), StartPositionTicks: 0}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("chapters = %#v, want %#v", got, want)
	}
}

func TestNormalizeSkipsTimestampOutsideVideoFile(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "upstream-b04614e", "video_ts.json"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "fixture.ts")
	if err := os.WriteFile(path, timestampBytes(1, 0x47, 0x47), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := probe.Normalize(context.Background(), raw, probe.Options{Path: path, Protocol: "File", VideoType: "Iso"})
	if err != nil {
		t.Fatal(err)
	}
	if info.Timestamp != nil {
		t.Fatalf("timestamp = %#v, want nil for non-VideoFile", info.Timestamp)
	}
}

func TestMpegTimestampContextCancellationInterruptsBlockedOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "blocked.ts")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	baseline := runtime.NumGoroutine()
	result := make(chan error, 1)
	go func() {
		_, err := probe.MpegTimestamp(ctx, path)
		result <- err
	}()
	select {
	case err := <-result:
		t.Fatalf("MpegTimestamp returned before cancellation: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context cancellation", err)
		}
		assertNoTimestampGoroutineLeak(t, baseline)
	case <-time.After(time.Second):
		t.Fatal("MpegTimestampContext did not return after cancellation during open")
	}
}

func TestNormalizeContextPropagatesTimestampCancellation(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "upstream-b04614e", "video_ts.json"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "blocked.ts")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	baseline := runtime.NumGoroutine()
	result := make(chan error, 1)
	go func() {
		_, normalizeErr := probe.Normalize(ctx, raw, probe.Options{Path: path, Protocol: "File", VideoType: "VideoFile"})
		result <- normalizeErr
	}()
	select {
	case err := <-result:
		t.Fatalf("NormalizeContext returned before cancellation: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context cancellation", err)
		}
		assertNoTimestampGoroutineLeak(t, baseline)
	case <-time.After(time.Second):
		t.Fatal("NormalizeContext did not return after timestamp-read cancellation")
	}
}

func TestMediaStreamPlaybackPropertiesThroughProbeSeam(t *testing.T) {
	one, compat, trueValue, profile := 1, 0, true, 5
	for _, tc := range []struct {
		name        string
		stream      playback.MediaStream
		wantRange   playback.VideoRange
		wantType    playback.VideoRangeType
		text        bool
		extractable bool
	}{
		{"HDR10+", playback.MediaStream{Type: playback.StreamTypeVideo, ColorTransfer: stringPointer("smpte2084"), Hdr10PlusPresentFlag: &trueValue}, playback.MediaVideoRangeHDR, playback.VideoRangeHDR10Plus, false, false},
		{"Dolby Vision profile 5 with compatibility", playback.MediaStream{Type: playback.StreamTypeVideo, DvProfile: &profile, RpuPresentFlag: &one, BlPresentFlag: &one, DvBlSignalCompatibilityID: &compat}, playback.MediaVideoRangeHDR, playback.VideoRangeDOVI, false, false},
		{"Dolby Vision nil compatibility falls through", playback.MediaStream{Type: playback.StreamTypeVideo, DvProfile: &profile, RpuPresentFlag: &one, BlPresentFlag: &one}, playback.MediaVideoRangeSDR, playback.VideoRangeSDR, false, false},
		{"HLG", playback.MediaStream{Type: playback.StreamTypeVideo, ColorTransfer: stringPointer("arib-std-b67")}, playback.MediaVideoRangeHDR, playback.VideoRangeHLG, false, false},
		{"text subtitle", playback.MediaStream{Type: playback.StreamTypeSubtitle, Codec: "srt"}, playback.MediaVideoRangeUnknown, playback.VideoRangeUnknown, true, true},
		{"PGS subtitle", playback.MediaStream{Type: playback.StreamTypeSubtitle, Codec: "pgssub"}, playback.MediaVideoRangeUnknown, playback.VideoRangeUnknown, false, true},
		{"VobSub subtitle", playback.MediaStream{Type: playback.StreamTypeSubtitle, Codec: "dvdsub"}, playback.MediaVideoRangeUnknown, playback.VideoRangeUnknown, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.stream.VideoRange(); got != tc.wantRange {
				t.Fatalf("VideoRange = %v, want %v", got, tc.wantRange)
			}
			if got := tc.stream.VideoRangeType(); got != tc.wantType {
				t.Fatalf("VideoRangeType = %v, want %v", got, tc.wantType)
			}
			if got := tc.stream.IsTextSubtitleStream(); got != tc.text {
				t.Fatalf("IsTextSubtitleStream = %t, want %t", got, tc.text)
			}
			if got := tc.stream.IsExtractableSubtitleStream(); got != tc.extractable {
				t.Fatalf("IsExtractableSubtitleStream = %t, want %t", got, tc.extractable)
			}
		})
	}
}

func TestNormalizeReadsHEVCTierFromFFprobeExtradata(t *testing.T) {
	const highTierHvcC = "\n00000000: 0121 0000 0000 0000 0000 0000 0000 0000  .!..............\n00000010: 0000 0000 0000 00  .......\n"
	const mainTierHvcC = "\n00000000: 0101 0000 0000 0000 0000 0000 0000 0000  .................\n00000010: 0000 0000 0000 00  .......\n"
	for _, tc := range []struct {
		name         string
		extradata    string
		wantHighTier bool
	}{
		{name: "high tier", extradata: highTierHvcC, wantHighTier: true},
		{name: "main tier", extradata: mainTierHvcC},
		{name: "unknown tier", extradata: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(map[string]any{
				"streams": []any{map[string]any{
					"index": 0, "codec_type": "video", "codec_name": "hevc", "profile": "Main 10",
					"width": 1920, "height": 1080, "level": 153, "extradata": tc.extradata,
				}},
				"format": map[string]any{"format_name": "matroska"},
			})
			if err != nil {
				t.Fatal(err)
			}
			info, err := probe.Normalize(context.Background(), raw, probe.Options{Path: "/media/hevc.mkv", Protocol: "File", VideoType: "VideoFile"})
			if err != nil {
				t.Fatalf("Normalize(): %v", err)
			}
			if len(info.MediaStreams) != 1 {
				t.Fatalf("MediaStreams = %#v, want one video stream", info.MediaStreams)
			}
			if got := info.MediaStreams[0].HighTier; got != tc.wantHighTier {
				t.Errorf("HighTier = %t, want %t", got, tc.wantHighTier)
			}
		})
	}
}

func TestMpegTimestampReadsExactly197Bytes(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		want playback.TransportStreamTimestamp
	}{
		{"packet begins at byte zero", timestampBytes(0x47, 0, 0), playback.TimestampNone},
		{"all zero leading packet", timestampBytes(0, 0x47, 0x47), playback.TimestampZero},
		{"valid first packet", timestampBytes(1, 0x47, 0x47), playback.TimestampValid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "sample.ts")
			if err := os.WriteFile(path, tc.data, 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := probe.MpegTimestamp(context.Background(), path)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("timestamp = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMpegTimestampReturnsUnexpectedEOFForTruncatedFIFO(t *testing.T) {
	path := filepath.Join(t.TempDir(), "truncated.ts")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	writer := make(chan error, 1)
	go func() {
		file, err := os.OpenFile(path, os.O_WRONLY, 0)
		if err == nil {
			_, err = file.Write(make([]byte, 196))
			closeErr := file.Close()
			if err == nil {
				err = closeErr
			}
		}
		writer <- err
	}()
	result := make(chan error, 1)
	go func() {
		_, err := probe.MpegTimestamp(context.Background(), path)
		result <- err
	}()
	select {
	case err := <-result:
		if !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("MpegTimestamp error = %v, want io.ErrUnexpectedEOF", err)
		}
	case <-time.After(time.Second):
		t.Fatal("MpegTimestamp did not return after truncated FIFO closed")
	}
	if err := <-writer; err != nil {
		t.Fatal(err)
	}
}

func TestNormalizeAddsTimestampForLocalTransportStream(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "upstream-b04614e", "video_ts.json"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "fixture.ts")
	if err := os.WriteFile(path, timestampBytes(1, 0x47, 0x47), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := probe.Normalize(context.Background(), raw, probe.Options{Path: path, Protocol: "File", VideoType: "VideoFile"})
	if err != nil {
		t.Fatal(err)
	}
	if info.Container != "ts" {
		t.Fatalf("container = %q, want ts", info.Container)
	}
	if info.Timestamp == nil || *info.Timestamp != playback.TimestampValid {
		t.Fatalf("timestamp = %#v, want Valid", info.Timestamp)
	}
}

func TestNormalizeAddsTimestampWhenProtocolDefaultsToFile(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "upstream-b04614e", "video_ts.json"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "fixture.ts")
	if err := os.WriteFile(path, timestampBytes(1, 0x47, 0x47), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := probe.Normalize(context.Background(), raw, probe.Options{Path: path, VideoType: "VideoFile"})
	if err != nil {
		t.Fatal(err)
	}
	if info.Timestamp == nil || *info.Timestamp != playback.TimestampValid {
		t.Fatalf("timestamp = %#v, want Valid when empty Protocol defaults to File", info.Timestamp)
	}
}

func normalizeFixture(t *testing.T, name string) probe.MediaInfo {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "upstream-b04614e", name))
	if err != nil {
		t.Fatal(err)
	}
	info, err := probe.Normalize(context.Background(), raw, probe.Options{Path: pinnedProbePath(name), Protocol: "File", VideoType: "VideoFile"})
	if err != nil {
		t.Fatalf("Normalize(%s): %v", name, err)
	}
	return info
}

func pinnedProbePath(name string) string {
	// Paths exactly mirror ProbeResultNormalizerTests.cs source invocations.
	switch name {
	case "video_metadata.json":
		return "Test Data/Probing/video_metadata.mkv"
	case "video_mp4_metadata.json":
		return "Test Data/Probing/video_mp4_metadata.mkv"
	case "video_ts.json", "video_web_like_mkv_with_subtitle.json":
		return "Test Data/Probing/video_metadata.mkv"
	case "video_webm.json":
		return "Test Data/Probing/video_metadata.webm"
	case "video_missing_video_bitrate.json":
		return "Test Data/Probing/video_missing_video_bitrate.mp4"
	case "video_nanosecond_duration_bitrate.json":
		return "Test Data/Probing/video_nanosecond_duration_bitrate.mkv"
	case "video_missing_video_bitrate_unknown_audio.json":
		return "Test Data/Probing/video_missing_video_bitrate_unknown_audio.mp4"
	default:
		panic("missing pinned upstream probe path for " + name)
	}
}

func assertJSONFields(t *testing.T, value any, want map[string]any) {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	for field, expected := range want {
		if actual, ok := got[field]; !ok || !reflect.DeepEqual(actual, expected) {
			t.Errorf("%s = %#v (present %t), want %#v", field, actual, ok, expected)
		}
	}
}

func timestampBytes(first, fourth, last byte) []byte {
	data := make([]byte, 197)
	data[0], data[4], data[196] = first, fourth, last
	return data
}

func stringPointer(value string) *string { return &value }

func assertNoTimestampGoroutineLeak(t *testing.T, baseline int) {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for runtime.NumGoroutine() > baseline {
		select {
		case <-deadline.C:
			t.Fatalf("timestamp cancellation left goroutines running: got %d, baseline %d", runtime.NumGoroutine(), baseline)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func TestNormalizePreservesPinnedMetadataTagOrderingAndLayerPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want string
	}{
		{name: "first stream tag wins case collision", raw: `{"streams":[{"index":0,"codec_type":"video","codec_name":"h264","width":1,"height":1,"tags":{"title":"first","TITLE":"second"}}],"format":{"format_name":"matroska"}}`, want: "first"},
		{name: "format tag overrides stream layer", raw: `{"streams":[{"index":0,"codec_type":"video","codec_name":"h264","width":1,"height":1,"tags":{"title":"stream"}}],"format":{"format_name":"matroska","tags":{"TITLE":"format"}}}`, want: "format"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info, err := probe.Normalize(context.Background(), []byte(tc.raw), probe.Options{Path: "/media/tag-order.mkv", Protocol: "File", VideoType: "VideoFile"})
			if err != nil {
				t.Fatal(err)
			}
			if got := info.Name; got == nil || *got != tc.want {
				t.Fatalf("Name = %v, want %q", got, tc.want)
			}
		})
	}
}

func TestNormalizeChapterTitleLookupRemainsCaseSensitive(t *testing.T) {
	for _, tc := range []struct {
		name string
		tags string
		want *string
	}{
		{name: "uppercase only", tags: `"TITLE":"upper only"`},
		{name: "ordered lowercase survives uppercase", tags: `"TITLE":"first","title":"second"`, want: stringPointer("second")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := []byte(`{"streams":[],"chapters":[{"start_time":"0","tags":{` + tc.tags + `}}],"format":{"format_name":"matroska"}}`)
			info, err := probe.Normalize(context.Background(), raw, probe.Options{Path: "/media/chapter-case.mkv", Protocol: "File", VideoType: "VideoFile"})
			if err != nil {
				t.Fatal(err)
			}
			if got := info.Chapters[0].Name; !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Chapter.Name = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestNormalizePreservesNonpositiveFormatBitrate(t *testing.T) {
	for _, value := range []string{"0", "-1"} {
		t.Run(value, func(t *testing.T) {
			raw := []byte(`{"streams":[{"index":0,"codec_type":"video","codec_name":"h264","width":1,"height":1,"bit_rate":"1000"},{"index":1,"codec_type":"audio","codec_name":"aac","bit_rate":"2000"}],"format":{"format_name":"matroska","bit_rate":"` + value + `"}}`)
			info, err := probe.Normalize(context.Background(), raw, probe.Options{Path: "/media/signed-bitrate.mkv", Protocol: "File", VideoType: "VideoFile"})
			if err != nil {
				t.Fatal(err)
			}
			if got := info.Bitrate; got == nil || *got != map[string]int{"0": 0, "-1": -1}[value] {
				t.Fatalf("Bitrate = %v, want raw format bit_rate %s without inference", got, value)
			}
		})
	}
}

func TestNormalizeParsesLongAndDayQualifiedTagDurations(t *testing.T) {
	for _, tc := range []struct {
		duration string
		want     int
	}{
		// TimeSpan treats a leading 25 as days, not wall-clock hours.
		{duration: "25:00:00", want: 333},
		{duration: "23:00:00", want: 8696},
		{duration: "1.01:00:00", want: 8000},
	} {
		t.Run(tc.duration, func(t *testing.T) {
			raw := []byte(`{"streams":[{"index":0,"codec_type":"video","codec_name":"h264","width":1,"height":1,"tags":{"NUMBER_OF_BYTES":"90000000","DURATION":"` + tc.duration + `"}}],"format":{"format_name":"matroska"}}`)
			info, err := probe.Normalize(context.Background(), raw, probe.Options{Path: "/media/long-duration.mkv", Protocol: "File", VideoType: "VideoFile"})
			if err != nil {
				t.Fatal(err)
			}
			if got := info.MediaStreams[0].BitRate; got == nil || *got != tc.want {
				if got == nil {
					t.Fatalf("tag-derived BitRate = nil, want %d for %s", tc.want, tc.duration)
				}
				t.Fatalf("tag-derived BitRate = %d, want %d for %s", *got, tc.want, tc.duration)
			}
		})
	}
}

func TestNormalizeAdjustsOffsetCreationTimeToUTC(t *testing.T) {
	for _, creationTime := range []string{"2021-09-13 22:42:42 +02:00", "2021-09-13 22:42:42+02:00"} {
		t.Run(creationTime, func(t *testing.T) {
			raw := []byte(`{"streams":[{"index":0,"codec_type":"video","codec_name":"h264","width":1,"height":1,"tags":{"creation_time":"` + creationTime + `"}}],"format":{"format_name":"matroska"}}`)
			info, err := probe.Normalize(context.Background(), raw, probe.Options{Path: "/media/offset-creation.mkv", Protocol: "File", VideoType: "VideoFile"})
			if err != nil {
				t.Fatal(err)
			}
			if got := info.ProductionYear; got == nil || *got != 2021 {
				t.Fatalf("ProductionYear = %v, want 2021", got)
			}
			if got := info.PremiereDate; got == nil || got.Format(time.RFC3339) != "2021-09-13T20:42:42Z" {
				t.Fatalf("PremiereDate = %v, want 2021-09-13T20:42:42Z", got)
			}
		})
	}
}

func TestNormalizePreservesEmptyGenreCollectionForDelimiterOnlyValue(t *testing.T) {
	raw := []byte(`{"streams":[],"format":{"format_name":"matroska","tags":{"genre":"///"}}}`)
	info, err := probe.Normalize(context.Background(), raw, probe.Options{Path: "/media/empty-genre.mkv", Protocol: "File", VideoType: "VideoFile"})
	if err != nil {
		t.Fatal(err)
	}
	if info.Genres == nil || len(info.Genres) != 0 {
		t.Fatalf("Genres = %#v, want nonnil empty collection", info.Genres)
	}
}

func TestNormalizeDoesNotInferTotalBitrateForAudioOnlySource(t *testing.T) {
	raw := []byte(`{"streams":[{"index":0,"codec_type":"audio","codec_name":"aac","bit_rate":"128000"}],"format":{"format_name":"mp3"}}`)
	info, err := probe.Normalize(context.Background(), raw, probe.Options{IsAudio: true, Path: "/media/audio-only.mp3", Protocol: "File"})
	if err != nil {
		t.Fatal(err)
	}
	if got := info.MediaStreams[0].BitRate; got == nil || *got != 128000 {
		t.Fatalf("stream BitRate = %v, want 128000", got)
	}
	if info.Bitrate != nil {
		t.Fatalf("MediaInfo.Bitrate = %d, want nil when no video stream exists", *info.Bitrate)
	}
}

func TestNormalizeIgnoresTagDurationBeyondTimeSpanRange(t *testing.T) {
	raw := []byte(`{"streams":[{"index":0,"codec_type":"video","codec_name":"h264","width":1,"height":1,"tags":{"NUMBER_OF_BYTES":"9223372036854775807","DURATION":"106751992.00:00:00"}}],"format":{"format_name":"matroska"}}`)
	info, err := probe.Normalize(context.Background(), raw, probe.Options{Path: "/media/too-long.mkv", Protocol: "File", VideoType: "VideoFile"})
	if err != nil {
		t.Fatal(err)
	}
	if got := info.MediaStreams[0].BitRate; got != nil {
		t.Fatalf("tag-derived BitRate = %d, want nil beyond .NET TimeSpan range", *got)
	}
}

func TestNormalizeRejectsOutOfRangeExplicitDayHourComponents(t *testing.T) {
	for _, duration := range []string{"0.25:00:00", "-1.25:00:00"} {
		t.Run(duration, func(t *testing.T) {
			raw := []byte(`{"streams":[{"index":0,"codec_type":"video","codec_name":"h264","width":1,"height":1,"tags":{"NUMBER_OF_BYTES":"90000000","DURATION":"` + duration + `"}}],"format":{"format_name":"matroska"}}`)
			info, err := probe.Normalize(context.Background(), raw, probe.Options{Path: "/media/invalid-day-hour.mkv", Protocol: "File", VideoType: "VideoFile"})
			if err != nil {
				t.Fatal(err)
			}
			if got := info.MediaStreams[0].BitRate; got != nil {
				t.Fatalf("tag-derived BitRate = %d, want nil for invalid TimeSpan %s", *got, duration)
			}
		})
	}
}

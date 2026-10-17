// SPDX-License-Identifier: GPL-2.0-only

package playback

// These tests port StreamInfo.cs @ b04614e18d2aeed8d3d3de920c381c6bd5b97bdb.
// The golden matrix was recorded from that exact source by goldenrec.

import (
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

type streamInfoGoldenCorpus struct {
	Cases []streamInfoGoldenCase `json:"cases"`
}

type streamInfoGoldenCase struct {
	ID      string                  `json:"id"`
	Profile string                  `json:"profile"`
	Source  string                  `json:"source"`
	Video   streamInfoGoldenOutcome `json:"video"`
	Audio   streamInfoGoldenOutcome `json:"audio"`
}

type streamInfoGoldenOutcome struct {
	StreamInfo json.RawMessage `json:"streamInfo"`
	URL        string          `json:"url"`
}

// TestStreamInfoGoldenMatrix round-trips every recorded goldenrec StreamInfo
// payload and checks URLs byte-for-byte: 627 profile/source pairs, 1,235
// serialized outcomes, and 19 explicitly absent no-stream audio outcomes.
func TestStreamInfoGoldenMatrix(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "golden", "streambuilder.json"))
	if err != nil {
		t.Fatal(err)
	}

	var corpus streamInfoGoldenCorpus
	if err := json.Unmarshal(data, &corpus); err != nil {
		t.Fatal(err)
	}
	if got, want := len(corpus.Cases), 627; got != want {
		t.Fatalf("golden corpus case count = %d, want %d", got, want)
	}
	profiles, sources := make(map[string]struct{}), make(map[string]struct{})
	for _, golden := range corpus.Cases {
		profiles[golden.Profile] = struct{}{}
		sources[golden.Source] = struct{}{}
	}
	if got, want := len(profiles), 19; got != want {
		t.Fatalf("golden profile count = %d, want %d", got, want)
	}
	if got, want := len(sources), 33; got != want {
		t.Fatalf("golden source count = %d, want %d", got, want)
	}
	populated, absent := 0, 0
	for _, golden := range corpus.Cases {
		for _, outcome := range []streamInfoGoldenOutcome{golden.Video, golden.Audio} {
			if len(outcome.StreamInfo) == 0 {
				absent++
			} else {
				populated++
			}
		}
	}
	if got, want := populated, 1235; got != want {
		t.Fatalf("golden populated outcome count = %d, want %d", got, want)
	}
	if got, want := absent, 19; got != want {
		t.Fatalf("golden absent outcome count = %d, want %d", got, want)
	}

	for _, golden := range corpus.Cases {
		for _, kind := range []struct {
			name string
			want streamInfoGoldenOutcome
		}{
			{name: "video", want: golden.Video},
			{name: "audio", want: golden.Audio},
		} {
			t.Run(golden.ID+"/"+kind.name, func(t *testing.T) {
				if len(kind.want.StreamInfo) == 0 {
					if kind.want.URL != "" {
						t.Fatalf("absent StreamInfo has URL %q", kind.want.URL)
					}
					return
				}
				var info StreamInfo
				if err := json.Unmarshal(kind.want.StreamInfo, &info); err != nil {
					t.Fatalf("unmarshal recorded StreamInfo: %v", err)
				}

				assertJSONEqual(t, kind.want.StreamInfo, mustMarshalJSON(t, info))
				if got := info.ToURL("media:", "ACCESSTOKEN", ""); got != kind.want.URL {
					t.Fatalf("ToURL bytes differ\n got: %q\nwant: %q", got, kind.want.URL)
				}
			})
		}
	}
}

// TestStreamInfoTargetPropertiesThroughQualifiedOptions covers every computed
// Target*/IsTarget* family in StreamInfo.cs:301-805. Qualifier options must
// win over bare-name fallbacks, while missing options preserve source values.
func TestStreamInfoTargetPropertiesThroughQualifiedOptions(t *testing.T) {
	info := decodeStreamInfo(t, `{
		"PlayMethod":"Transcode",
		"Container":"m2ts",
		"AudioCodecs":["aac"],
		"VideoCodecs":["h264"],
		"AudioSampleRate":44100,
		"AudioBitrate":192000,
		"VideoBitrate":4000000,
		"MaxFramerate":24,
		"MaxWidth":1280,
		"MaxHeight":720,
		"RunTimeTicks":100000000,
		"GlobalMaxAudioChannels":6,
		"MediaSource":{
			"Size":999,
			"Timestamp":"Valid",
			"MediaStreams":[
				{"Type":"Video","Index":0,"Codec":"h264","BitDepth":8,"RefFrames":2,"ReferenceFrameRate":30,"Level":3.1,"Profile":"Main","VideoRangeType":"SDR","CodecTag":"avc1","BitRate":5000000,"PacketLength":188,"IsAnamorphic":true,"IsInterlaced":true,"IsAVC":true,"Width":1920,"Height":1080},
				{"Type":"Audio","Index":1,"Codec":"aac","BitDepth":16,"SampleRate":48000,"BitRate":256000,"Channels":2,"IsDefault":true}
			]
		}
	}`)

	// StreamInfo.cs:815-868. Case-insensitive duplicate keys replace the old
	// value; qualified options take precedence over an unqualified fallback.
	info.SetOptionName("profile", "Fallback")
	info.SetOption("H264", "Profile", "Old")
	info.SetOption("h264", "profile", "High 10")
	info.SetOption("h264", "videobitdepth", "10")
	info.SetOption("h264", "maxrefframes", "4")
	info.SetOption("h264", "level", "4.1")
	info.SetOption("h264", "rangetype", "HDR10")
	info.SetOption("h264", "deinterlace", "TRUE")
	info.SetOption("aac", "audiobitdepth", "24")
	info.SetOption("aac", "audiochannels", "8")
	// Upstream TargetAudioChannels deliberately (and surprisingly) calls
	// GetTargetRefFrames(codec), not GetTargetAudioChannels(codec), :530-535.
	info.SetOption("aac", "maxrefframes", "5")

	if got, want := info.GetOption("h264", "profile"), "High 10"; got != want {
		t.Fatalf("qualified duplicate option = %q, want %q", got, want)
	}
	if got, want := info.GetOption("missing", "profile"), "Fallback"; got != want {
		t.Fatalf("missing qualifier fallback = %q, want %q", got, want)
	}
	if got := info.GetOption("h264", "does-not-exist"); got != "" {
		t.Fatalf("absent option = %q, want empty", got)
	}

	got := streamInfoJSONObject(t, info)
	for property, want := range map[string]any{
		"IsDirectStream":         false,
		"TargetAudioSampleRate":  float64(44100),
		"TargetAudioBitDepth":    float64(24),
		"TargetVideoBitDepth":    float64(10),
		"TargetRefFrames":        float64(4),
		"TargetFramerate":        float64(24),
		"TargetVideoLevel":       float64(4.1),
		"TargetVideoProfile":     "High 10",
		"TargetVideoRangeType":   "HDR10",
		"TargetAudioBitrate":     float64(192000),
		"TargetAudioChannels":    float64(5),
		"TargetAudioCodec":       []any{"aac"},
		"TargetVideoCodec":       []any{"h264"},
		"TargetSize":             float64(41920000),
		"TargetVideoBitrate":     float64(4000000),
		"TargetTimestamp":        "Valid",
		"TargetTotalBitrate":     float64(4192000),
		"IsTargetAnamorphic":     false,
		"IsTargetInterlaced":     false,
		"IsTargetAVC":            true,
		"TargetWidth":            float64(1280),
		"TargetHeight":           float64(720),
		"TargetVideoStreamCount": float64(1),
		"TargetAudioStreamCount": float64(1),
	} {
		if value, ok := got[property]; !ok || !reflect.DeepEqual(value, want) {
			t.Errorf("%s = %#v (present %t), want %#v", property, value, ok, want)
		}
	}
	for _, property := range []string{"TargetPacketLength", "TargetVideoCodecTag"} {
		if value, ok := got[property]; ok {
			t.Errorf("%s = %#v, want omitted for null", property, value)
		}
	}
}

// TestStreamInfoTargetPropertiesDefaultToSource keeps the absent-option and
// direct-stream branch distinct from the transcode option branches above.
func TestStreamInfoTargetPropertiesDefaultToSource(t *testing.T) {
	info := decodeStreamInfo(t, `{
		"PlayMethod":"DirectPlay",
		"Container":"mp4",
		"MediaSource":{"Size":1234,"Timestamp":"Valid","MediaStreams":[
			{"Type":"Video","Index":0,"Codec":"h264","BitDepth":8,"RefFrames":3,"ReferenceFrameRate":29.97,"Level":4,"Profile":"High","VideoRangeType":"SDR","CodecTag":"avc1","BitRate":3000000,"PacketLength":188,"IsAnamorphic":true,"IsInterlaced":true,"IsAVC":true,"Width":1920,"Height":1080},
			{"Type":"Audio","Index":1,"Codec":"ac3","BitDepth":16,"SampleRate":48000,"BitRate":384000,"Channels":6,"IsDefault":true}
		]}
	}`)

	got := streamInfoJSONObject(t, info)
	for property, want := range map[string]any{
		"IsDirectStream":         true,
		"TargetAudioSampleRate":  float64(48000),
		"TargetAudioBitDepth":    float64(16),
		"TargetVideoBitDepth":    float64(8),
		"TargetRefFrames":        float64(3),
		"TargetFramerate":        float64(29.97),
		"TargetVideoLevel":       float64(4),
		"TargetPacketLength":     float64(188),
		"TargetVideoProfile":     "High",
		"TargetVideoRangeType":   "SDR",
		"TargetVideoCodecTag":    "avc1",
		"TargetAudioBitrate":     float64(384000),
		"TargetAudioChannels":    float64(6),
		"TargetAudioCodec":       []any{"ac3"},
		"TargetVideoCodec":       []any{"h264"},
		"TargetSize":             float64(1234),
		"TargetVideoBitrate":     float64(3000000),
		"TargetTimestamp":        "Valid",
		"TargetTotalBitrate":     float64(3384000),
		"IsTargetAnamorphic":     true,
		"IsTargetInterlaced":     true,
		"IsTargetAVC":            true,
		"TargetWidth":            float64(1920),
		"TargetHeight":           float64(1080),
		"TargetVideoStreamCount": float64(1),
		"TargetAudioStreamCount": float64(1),
	} {
		if value, ok := got[property]; !ok || !reflect.DeepEqual(value, want) {
			t.Errorf("%s = %#v (present %t), want %#v", property, value, ok, want)
		}
	}
}

// TestStreamInfoToURLFixedOrderAndRawEscaping locks the unusual upstream
// contract: values are appended raw except ApiKey, whose opaque value is query
// escaped; stream-option spaces are removed; RequireAvc/VBR use lower-case.
func TestStreamInfoToURLFixedOrderAndRawEscaping(t *testing.T) {
	info := decodeStreamInfo(t, `{
		"ItemId":"11d229b7-2d48-4b95-9f9b-49f6ab75e613",
		"PlayMethod":"Transcode",
		"MediaType":"Video",
		"Container":"mkv",
		"SubProtocol":"http",
		"DeviceProfileId":"profile /?&=+",
		"DeviceId":"device /?&=+",
		"MediaSource":{"Id":"source /?&=+","LiveStreamId":"live /?&=+","ETag":"tag /?&=+"},
		"AudioCodecs":["aac","ac3"],
		"VideoCodecs":["h264","hevc"],
		"AudioStreamIndex":1,
		"SubtitleStreamIndex":2,
		"SubtitleDeliveryMethod":"Embed",
		"SubtitleCodecs":["srt","ass"],
		"VideoBitrate":4000000,
		"AudioBitrate":192000,
		"AudioSampleRate":48000,
		"MaxFramerate":23.976,
		"MaxWidth":1920,
		"MaxHeight":1080,
		"StartPositionTicks":123,
		"PlaySessionId":"session /?&=+",
		"RequireNonAnamorphic":true,
		"TranscodingMaxAudioChannels":6,
		"EnableSubtitlesInManifest":true,
		"EnableMpegtsM2TsMode":true,
		"EstimateContentLength":true,
		"TranscodeSeekInfo":"Bytes",
		"CopyTimestamps":true,
		"RequireAvc":false,
		"EnableAudioVbrEncoding":false
	}`)
	info.SetOptionName("Custom Option", "x y&z?=+")

	want := "media:/videos/11d229b7-2d48-4b95-9f9b-49f6ab75e613/stream.mkv?DeviceProfileId=profile /?&=+&DeviceId=device /?&=+&MediaSourceId=source /?&=+&VideoCodec=h264,hevc&AudioCodec=aac,ac3&AudioStreamIndex=1&SubtitleStreamIndex=2&VideoBitrate=4000000&AudioBitrate=192000&AudioSampleRate=48000&MaxFramerate=23.976&MaxWidth=1920&MaxHeight=1080&StartTimeTicks=123&PlaySessionId=session /?&=+&ApiKey=token+%2F%3F%26%3D%2B&LiveStreamId=live /?&=+&RequireNonAnamorphic=True&TranscodingMaxAudioChannels=6&EnableSubtitlesInManifest=True&EnableMpegtsM2TsMode=True&EstimateContentLength=True&TranscodeSeekInfo=Bytes&CopyTimestamps=True&RequireAvc=false&EnableAudioVbrEncoding=false&Tag=tag /?&=+&SubtitleMethod=Embed&SubtitleCodec=srt,ass&Custom Option=xy&z?=+&tail=raw"
	if got := info.ToURL("media://", "token /?&=+", "&tail=raw"); got != want {
		t.Fatalf("ToURL bytes differ\n got: %q\nwant: %q", got, want)
	}
}

func TestStreamInfoToURLEscapesOpaqueAccessToken(t *testing.T) {
	const apiKey = "alpha+beta&role=admin% xy/雪"
	info := decodeStreamInfo(t, `{
		"ItemId":"11d229b7-2d48-4b95-9f9b-49f6ab75e613",
		"PlayMethod":"DirectPlay",
		"MediaType":"Video",
		"Container":"mp4"
	}`)

	got := info.ToURL("media://", apiKey, "")
	want := "media:/videos/11d229b7-2d48-4b95-9f9b-49f6ab75e613/stream.mp4?Static=true&ApiKey=alpha%2Bbeta%26role%3Dadmin%25+xy%2F%E9%9B%AA"
	if got != want {
		t.Fatalf("ToURL bytes = %q, want canonical URI %q", got, want)
	}

	uri, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse emitted URI: %v", err)
	}
	query := uri.Query()
	if got := query["ApiKey"]; !reflect.DeepEqual(got, []string{apiKey}) {
		t.Fatalf("ApiKey values = %#v, want exactly %#v", got, []string{apiKey})
	}
	if got := query["Static"]; !reflect.DeepEqual(got, []string{"true"}) {
		t.Fatalf("Static values = %#v, want unchanged %#v", got, []string{"true"})
	}
	if len(query) != 2 {
		t.Fatalf("query fields = %#v, want only Static and ApiKey", query)
	}
}

func decodeStreamInfo(t *testing.T, input string) StreamInfo {
	t.Helper()
	var info StreamInfo
	if err := json.Unmarshal([]byte(input), &info); err != nil {
		t.Fatalf("unmarshal StreamInfo fixture: %v", err)
	}
	return info
}

func streamInfoJSONObject(t *testing.T, info StreamInfo) map[string]any {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal(mustMarshalJSON(t, info), &got); err != nil {
		t.Fatal(err)
	}
	return got
}

func mustMarshalJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func assertJSONEqual(t *testing.T, wantBytes, gotBytes []byte) {
	t.Helper()
	var want, got any
	if err := json.Unmarshal(wantBytes, &want); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(gotBytes, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("StreamInfo JSON differs: %s", firstJSONDifference("", want, got))
	}
}

func firstJSONDifference(path string, want, got any) string {
	if !reflect.DeepEqual(want, got) {
		switch wantValue := want.(type) {
		case map[string]any:
			gotValue, ok := got.(map[string]any)
			if !ok {
				return fmt.Sprintf("%s: got %T, want object", path, got)
			}
			for key, wantField := range wantValue {
				gotField, present := gotValue[key]
				fieldPath := key
				if path != "" {
					fieldPath = path + "." + key
				}
				if !present {
					return fmt.Sprintf("%s missing (want %#v)", fieldPath, wantField)
				}
				if diff := firstJSONDifference(fieldPath, wantField, gotField); diff != "" {
					return diff
				}
			}
			for key, gotField := range gotValue {
				if _, present := wantValue[key]; !present {
					fieldPath := key
					if path != "" {
						fieldPath = path + "." + key
					}
					return fmt.Sprintf("%s unexpectedly present (%#v)", fieldPath, gotField)
				}
			}
		case []any:
			gotValue, ok := got.([]any)
			if !ok {
				return fmt.Sprintf("%s: got %T, want array", path, got)
			}
			if len(wantValue) != len(gotValue) {
				return fmt.Sprintf("%s length = %d, want %d", path, len(gotValue), len(wantValue))
			}
			for index, wantElement := range wantValue {
				if diff := firstJSONDifference(fmt.Sprintf("%s[%d]", path, index), wantElement, gotValue[index]); diff != "" {
					return diff
				}
			}
		default:
			return fmt.Sprintf("%s = %#v, want %#v", path, got, want)
		}
	}
	return ""
}

// Additional source-pinned regressions from StreamInfo.cs @ b04614e and
// MediaStream.cs:539-554. They lock .NET, rather than Go, scalar semantics.
func TestStreamInfoDotNetOptionAndTargetScalarSemantics(t *testing.T) {
	info := decodeStreamInfo(t, `{"PlayMethod":"Transcode","VideoCodecs":["h264"],"AudioCodecs":["aac"]}`)
	info.SetOption("h264", "videobitdepth", "2147483648")
	info.SetOption("h264", "maxrefframes", "-2147483649")
	info.SetOption("h264", "level", "1,234.5")
	info.SetOption("h264", "rangetype", "99")
	info.SetOption("h264", "profile", "")
	info.SetOption("aac", "audiobitdepth", "2147483648")

	if got := info.GetTargetVideoBitDepth("h264"); got != nil {
		t.Errorf("Int32-overflow video bit depth = %v, want nil", *got)
	}
	if got := info.GetTargetRefFrames("h264"); got != nil {
		t.Errorf("Int32-underflow ref frames = %v, want nil", *got)
	}
	if got := info.targetAudioBitDepth("aac"); got != nil {
		t.Errorf("Int32-overflow audio bit depth = %v, want nil", *got)
	}
	if got := info.GetTargetVideoLevel("h264"); got == nil || *got != 1234.5 {
		t.Errorf("InvariantCulture thousands level = %v, want 1234.5", got)
	}
	if got, want := info.TargetVideoRangeType(), VideoRangeType(99); got != want {
		t.Errorf("Enum.TryParse undefined numeric = %v, want %v", got, want)
	}
	if got := info.TargetVideoProfile(); got == nil || *got != "" {
		t.Errorf("explicit empty target profile = %v, want non-nil empty string", got)
	}
}

func TestStreamOptionsUsesOrdinalIgnoreCaseNotUnicodeFolding(t *testing.T) {
	info := NewStreamInfo()
	info.SetOptionName("İ", "dotted")
	info.SetOptionName("i", "ascii")
	info.SetOptionName("I", "replacement")

	if got, want := info.GetOptionName("İ"), "dotted"; got != want {
		t.Errorf("dotted-I option = %q, want %q", got, want)
	}
	if got, want := info.GetOptionName("i"), "replacement"; got != want {
		t.Errorf("ASCII-i option = %q, want %q", got, want)
	}
	var options map[string]string
	if err := json.Unmarshal(mustMarshalJSON(t, info.StreamOptions), &options); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(options, map[string]string{"İ": "dotted", "i": "replacement"}) {
		t.Errorf("option insertion/replacement wire shape = %#v", options)
	}
}

func TestStreamInfoTargetTimestampAndSizeDotNetEdges(t *testing.T) {
	directM2TS := decodeStreamInfo(t, `{"PlayMethod":"DirectPlay","Container":"m2ts","MediaSource":{"MediaStreams":[]}}`)
	if got, want := directM2TS.TargetTimestamp(), TimestampNone; got != want {
		t.Errorf("direct m2ts with a non-nil source but nil Timestamp = %v, want %v", got, want)
	}

	tie := decodeStreamInfo(t, `{"PlayMethod":"Transcode","AudioBitrate":1,"RunTimeTicks":5000000}`)
	if got := tie.TargetSize(); got == nil || *got != 0 {
		t.Errorf("Convert.ToInt64 0.5 tie = %v, want 0 (ties-to-even)", got)
	}
}

func TestMediaStreamReferenceFrameRateIsComputedAndWireSerialized(t *testing.T) {
	var stream MediaStream
	if err := json.Unmarshal([]byte(`{"Type":"Video","AverageFrameRate":1000,"RealFrameRate":24,"ReferenceFrameRate":99}`), &stream); err != nil {
		t.Fatal(err)
	}
	if got := stream.ReferenceFrameRate(); got == nil || *got != 24 {
		t.Errorf("ReferenceFrameRate getter = %v, want RealFrameRate fallback 24", got)
	}
	var wire map[string]any
	if err := json.Unmarshal(mustMarshalJSON(t, stream), &wire); err != nil {
		t.Fatal(err)
	}
	if got, want := wire["ReferenceFrameRate"], float64(24); got != want {
		t.Errorf("serialized ReferenceFrameRate = %#v, want %#v", got, want)
	}
}

func TestMediaStreamHEVCTierIsStoredButNotExposedOnWire(t *testing.T) {
	stream := MediaStream{Codec: "hevc", HighTier: true}
	encoded := mustMarshalJSON(t, stream)
	var wire map[string]any
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	if _, exists := wire["HevcHighTier"]; exists {
		t.Errorf("public media stream includes internal HevcHighTier field: %s", encoded)
	}
}

func TestPlaybackModelWireDefaultsAndEnumCompatibility(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input string
		want  string
	}{
		{name: "half-side-by-side", input: `"HalfSideBySide"`, want: `"HalfSideBySide"`},
		{name: "embedded-image", input: `"EmbeddedImage"`, want: `"EmbeddedImage"`},
		{name: "undefined-numeric", input: `256`, want: `256`},
		{name: "numeric-string", input: `"256"`, want: `256`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var value StreamType
			if tc.name == "half-side-by-side" {
				var format Video3DFormat
				if err := json.Unmarshal([]byte(tc.input), &format); err != nil {
					t.Fatal(err)
				}
				got := string(mustMarshalJSON(t, format))
				if got != tc.want {
					t.Errorf("Video3DFormat wire = %s, want %s", got, tc.want)
				}
				return
			}
			if tc.name == "embedded-image" {
				if err := json.Unmarshal([]byte(tc.input), &value); err != nil {
					t.Fatal(err)
				}
				got := string(mustMarshalJSON(t, value))
				if got != tc.want {
					t.Errorf("StreamType wire = %s, want %s", got, tc.want)
				}
				return
			}
			if err := json.Unmarshal([]byte(tc.input), &value); err != nil {
				t.Fatal(err)
			}
			if got := string(mustMarshalJSON(t, value)); got != tc.want {
				t.Errorf("undefined StreamType wire = %s, want %s", got, tc.want)
			}
		})
	}

	var source MediaSourceInfo
	if err := json.Unmarshal([]byte(`{}`), &source); err != nil {
		t.Fatal(err)
	}
	if !source.SupportsTranscoding || !source.SupportsDirectStream || !source.SupportsDirectPlay {
		t.Errorf("MediaSourceInfo defaults = transcode:%t directStream:%t directPlay:%t, all want true", source.SupportsTranscoding, source.SupportsDirectStream, source.SupportsDirectPlay)
	}
	var sourceWire map[string]any
	if err := json.Unmarshal(mustMarshalJSON(t, source), &sourceWire); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"MediaStreams", "MediaAttachments", "Formats", "RequiredHttpHeaders"} {
		if _, ok := sourceWire[name]; !ok {
			t.Errorf("MediaSourceInfo default wire missing %s", name)
		}
	}
}

func TestStreamInfoToURLAdditionalOracleCases(t *testing.T) {
	itemID := "11d229b7-2d48-4b95-9f9b-49f6ab75e613"
	for _, tc := range []struct {
		name, input, token, query, want string
	}{
		{
			// StreamInfo.cs:1089-1093 always emits the two false transcode
			// values, so a literal query-only URL is unreachable by design.
			name:  "query-appended-empty-token",
			input: `{"ItemId":"` + itemID + `","PlayMethod":"Transcode","MediaType":"Video"}`,
			query: "&tail=raw",
			want:  "media:/videos/" + itemID + "/stream?RequireAvc=false&EnableAudioVbrEncoding=false&tail=raw",
		},
		{
			name:  "hls-segments",
			input: `{"ItemId":"` + itemID + `","PlayMethod":"DirectPlay","MediaType":"Video","Container":"ts","SubProtocol":"hls","SegmentLength":5,"MinSegments":2}`,
			want:  "media:/videos/" + itemID + "/master.m3u8?Static=true&SegmentContainer=ts&SegmentLength=5&MinSegments=2",
		},
		{
			name:  "http-start-time",
			input: `{"ItemId":"` + itemID + `","PlayMethod":"DirectPlay","MediaType":"Video","Container":"mp4","StartPositionTicks":123}`,
			want:  "media:/videos/" + itemID + "/stream.mp4?Static=true&StartTimeTicks=123",
		},
		{
			name:  "external-subtitle-always-burn",
			input: `{"ItemId":"` + itemID + `","PlayMethod":"DirectPlay","MediaType":"Video","SubtitleStreamIndex":2,"SubtitleDeliveryMethod":"External","AlwaysBurnInSubtitleWhenTranscoding":true}`,
			want:  "media:/videos/" + itemID + "/stream?Static=true&SubtitleStreamIndex=2",
		},
		{
			name:  "invariant-scientific-float",
			input: `{"ItemId":"` + itemID + `","PlayMethod":"DirectPlay","MediaType":"Video","MaxFramerate":1e-10}`,
			want:  "media:/videos/" + itemID + "/stream?Static=true&MaxFramerate=1E-10",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info := decodeStreamInfo(t, tc.input)
			if got := info.ToURL("media:", tc.token, tc.query); got != tc.want {
				t.Errorf("ToURL = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPlaybackWireNullableStringsAndNumericStrings(t *testing.T) {
	var explicitEmpty, omitted MediaStream
	if err := json.Unmarshal([]byte(`{"Profile":""}`), &explicitEmpty); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`{}`), &omitted); err != nil {
		t.Fatal(err)
	}
	var presentWire, omittedWire map[string]any
	if err := json.Unmarshal(mustMarshalJSON(t, explicitEmpty), &presentWire); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(mustMarshalJSON(t, omitted), &omittedWire); err != nil {
		t.Fatal(err)
	}
	if got, ok := presentWire["Profile"]; !ok || got != "" {
		t.Errorf("explicit empty nullable Profile wire = %#v (present %t), want present empty", got, ok)
	}
	if got, ok := omittedWire["Profile"]; ok {
		t.Errorf("omitted nullable Profile wire = %#v, want omitted", got)
	}

	info := decodeStreamInfo(t, `{"StartPositionTicks":"2","AudioBitrate":"1","MaxWidth":"1280","MaxFramerate":"23.5"}`)
	if info.StartPositionTicks != 2 || info.AudioBitrate == nil || *info.AudioBitrate != 1 || info.MaxWidth == nil || *info.MaxWidth != 1280 || info.MaxFramerate == nil || *info.MaxFramerate != 23.5 {
		t.Errorf("AllowReadingFromString scalar values were not retained: %#v", info)
	}
}

func TestStreamInfoAndNestedWireEmptyNullContracts(t *testing.T) {
	// MediaSourceInfo.Id is nullable upstream: an absent Id omits MediaSourceId,
	// while an explicitly present empty Id must serialize as an empty string.
	for _, sourceJSON := range []string{`{}`, `{"Id":null}`} {
		absent := decodeStreamInfo(t, `{"MediaSource":`+sourceJSON+`}`)
		var absentWire map[string]any
		if err := json.Unmarshal(mustMarshalJSON(t, absent), &absentWire); err != nil {
			t.Fatal(err)
		}
		if value, present := absentWire["MediaSourceId"]; present {
			t.Errorf("nullable MediaSource.Id %s serialized MediaSourceId %#v, want omitted", sourceJSON, value)
		}
	}
	info := decodeStreamInfo(t, `{"MediaSource":{"Id":""}}`)
	var infoWire map[string]any
	if err := json.Unmarshal(mustMarshalJSON(t, info), &infoWire); err != nil {
		t.Fatal(err)
	}
	if got, ok := infoWire["MediaSourceId"]; !ok || got != "" {
		t.Errorf("top-level empty MediaSourceId = %#v (present %t), want present empty string", got, ok)
	}

	var source MediaSourceInfo
	if err := json.Unmarshal([]byte(`{"Id":"","MediaStreams":null,"MediaAttachments":null,"Formats":null,"RequiredHttpHeaders":null}`), &source); err != nil {
		t.Fatal(err)
	}
	var sourceWire map[string]any
	if err := json.Unmarshal(mustMarshalJSON(t, source), &sourceWire); err != nil {
		t.Fatal(err)
	}
	if got, ok := sourceWire["Id"]; !ok || got != "" {
		t.Errorf("nested nonnullable empty MediaSourceInfo.Id = %#v (present %t), want present empty", got, ok)
	}
	for _, field := range []string{"MediaStreams", "MediaAttachments", "Formats", "RequiredHttpHeaders"} {
		if value, present := sourceWire[field]; present {
			t.Errorf("explicit-null %s = %#v, want omitted under WhenWritingNull", field, value)
		}
	}

	var stream MediaStream
	if err := json.Unmarshal([]byte(`{"Codec":"","Type":"Audio"}`), &stream); err != nil {
		t.Fatal(err)
	}
	var streamWire map[string]any
	if err := json.Unmarshal(mustMarshalJSON(t, stream), &streamWire); err != nil {
		t.Fatal(err)
	}
	if got, ok := streamWire["Codec"]; !ok || got != "" {
		t.Errorf("nested nonnullable empty MediaStream.Codec = %#v (present %t), want present empty", got, ok)
	}
	if got, ok := streamWire["DisplayTitle"]; !ok || got != "" {
		t.Errorf("empty DisplayTitle getter = %#v (present %t), want present empty", got, ok)
	}
}

func TestMediaStreamFirstRuneCasingIsUnicodeSafe(t *testing.T) {
	if got, want := streamFirstToUpper("éclair"), "Éclair"; got != want {
		t.Errorf("first-rune casing = %q, want %q", got, want)
	}
}

func TestStreamInfoDotNetScalarOverflowAndSpecialFormatEdges(t *testing.T) {
	canonical := decodeStreamInfo(t, `{"PlayMethod":"DirectPlay","StartPositionTicks":"0002"}`)
	if got, want := canonical.ToURL("media:", "", ""), "media:/audio/00000000-0000-0000-0000-000000000000/stream?Static=true&StartTimeTicks=2"; got != want {
		t.Errorf("numeric string canonical URL = %q, want %q", got, want)
	}

	var overflow StreamInfo
	if err := json.Unmarshal([]byte(`{"AudioBitrate":"2147483648"}`), &overflow); err == nil {
		t.Error("Int32 JSON string overflow accepted, want error")
	}

	total := decodeStreamInfo(t, `{"PlayMethod":"Transcode","AudioBitrate":2147483647,"VideoBitrate":1}`)
	if got, want := total.TargetTotalBitrate(), -2147483648; got != want {
		t.Errorf("unchecked Int32 total = %d, want %d", got, want)
	}

	seek := decodeStreamInfo(t, `{"PlayMethod":"Transcode","TranscodeSeekInfo":99}`)
	if got, want := seek.ToURL("media:", "", ""), "media:/audio/00000000-0000-0000-0000-000000000000/stream?TranscodeSeekInfo=99&RequireAvc=false&EnableAudioVbrEncoding=false"; got != want {
		t.Errorf("undefined TranscodeSeekInfo URL = %q, want %q", got, want)
	}
	var seekWire map[string]any
	if err := json.Unmarshal(mustMarshalJSON(t, seek), &seekWire); err != nil {
		t.Fatal(err)
	}
	if got, want := seekWire["TranscodeSeekInfo"], float64(99); got != want {
		t.Errorf("undefined TranscodeSeekInfo JSON = %#v, want unquoted numeric %#v", got, want)
	}

	inf := float32(math.Inf(1))
	special := NewStreamInfo()
	special.PlayMethod = PlayMethodDirectPlay
	special.MaxFramerate = &inf
	if got, want := special.ToURL("media:", "", ""), "media:/audio/00000000-0000-0000-0000-000000000000/stream?Static=true&MaxFramerate=Infinity"; got != want {
		t.Errorf("positive infinity URL = %q, want %q", got, want)
	}
}

// StreamInfo.cs constructor creates empty codec arrays, while JsonDefaults'
// WhenWritingNull omits explicitly supplied null arrays.
func TestStreamInfoCodecCollectionPresence(t *testing.T) {
	var omitted, explicitNull StreamInfo
	if err := json.Unmarshal([]byte(`{}`), &omitted); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`{"AudioCodecs":null,"VideoCodecs":null,"SubtitleCodecs":null}`), &explicitNull); err != nil {
		t.Fatal(err)
	}
	var omittedWire, nullWire map[string]any
	if err := json.Unmarshal(mustMarshalJSON(t, omitted), &omittedWire); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(mustMarshalJSON(t, explicitNull), &nullWire); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"AudioCodecs", "VideoCodecs", "SubtitleCodecs"} {
		if got, present := omittedWire[name]; !present || !reflect.DeepEqual(got, []any{}) {
			t.Errorf("omitted %s = %#v (present %t), want constructor empty array", name, got, present)
		}
		if got, present := nullWire[name]; present {
			t.Errorf("explicit-null %s = %#v, want omitted", name, got)
		}
	}
}

// Numeric strings use upstream JsonNumberHandling.AllowReadingFromString, but
// each nested field retains its declared C# width and numeric grammar.
func TestNestedNumericWireWidthsAndGrammar(t *testing.T) {
	var stream MediaStream
	if err := json.Unmarshal([]byte(`{"BitRate":"+1","AverageFrameRate":"+1.5","Channels":null}`), &stream); err != nil {
		t.Fatalf("unmarshal signed nested scalars: %v", err)
	}
	if stream.BitRate == nil || *stream.BitRate != 1 || stream.AverageFrameRate == nil || *stream.AverageFrameRate != 1.5 || stream.Channels != nil {
		t.Errorf("nested signed/nullable values = %#v", stream)
	}
	var streamWire map[string]any
	if err := json.Unmarshal(mustMarshalJSON(t, stream), &streamWire); err != nil {
		t.Fatal(err)
	}
	if got, want := streamWire["BitRate"], float64(1); got != want {
		t.Errorf("canonical BitRate wire = %#v, want %#v", got, want)
	}
	if got, want := streamWire["AverageFrameRate"], float64(1.5); got != want {
		t.Errorf("canonical AverageFrameRate wire = %#v, want %#v", got, want)
	}
	if value, present := streamWire["Channels"]; present {
		t.Errorf("nullable null Channels wire = %#v, want omitted", value)
	}

	for _, input := range []string{
		`{"BitRate":"2147483648"}`,
		`{"BitRate":"-2147483649"}`,
		`{"AverageFrameRate":"3.5E38"}`,
	} {
		var invalid MediaStream
		if err := json.Unmarshal([]byte(input), &invalid); err == nil {
			t.Errorf("nested declared-width overflow %s accepted", input)
		}
	}

	var source MediaSourceInfo
	if err := json.Unmarshal([]byte(`{"Size":"+9223372036854775807","RunTimeTicks":"-1","BufferMs":null}`), &source); err != nil {
		t.Fatalf("unmarshal Int64 nested scalars: %v", err)
	}
	if source.Size == nil || *source.Size != 9223372036854775807 || source.RunTimeTicks == nil || *source.RunTimeTicks != -1 || source.BufferMs != nil {
		t.Errorf("nested Int64/nullable values = %#v", source)
	}
	for _, input := range []string{
		`{"Size":"9223372036854775808"}`,
		`{"RunTimeTicks":"-9223372036854775809"}`,
	} {
		var invalid MediaSourceInfo
		if err := json.Unmarshal([]byte(input), &invalid); err == nil {
			t.Errorf("nested Int64 overflow %s accepted", input)
		}
	}
}

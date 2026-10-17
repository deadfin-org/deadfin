// SPDX-License-Identifier: GPL-2.0-only

package probe

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// boolString ports Jellyfin's JsonBoolStringConverter, which exists because
// ffprobe writes fields such as is_avc as either JSON booleans or "true"/"false".
type boolString bool

func (b *boolString) UnmarshalJSON(data []byte) error {
	var value bool
	if err := json.Unmarshal(data, &value); err == nil {
		*b = boolString(value)
		return nil
	}
	var text string
	if err := json.Unmarshal(data, &text); err != nil {
		return fmt.Errorf("ffprobe boolean: %w", err)
	}
	switch text {
	case "true":
		*b = true
	case "false":
		*b = false
	default:
		return fmt.Errorf("invalid ffprobe boolean %q", text)
	}
	return nil
}

// intString accepts the number-or-string scalar encoding emitted by different
// ffprobe builds for integer stream facts such as bits_per_raw_sample.
type intString int32

func (n *intString) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		return fmt.Errorf("ffprobe integer: null")
	}
	var number int32
	if err := json.Unmarshal(data, &number); err == nil {
		*n = intString(number)
		return nil
	}
	var text string
	if err := json.Unmarshal(data, &text); err != nil {
		return fmt.Errorf("ffprobe integer: %w", err)
	}
	value, err := strconv.ParseInt(text, 10, 32)
	if err != nil {
		return fmt.Errorf("ffprobe integer %q: %w", text, err)
	}
	*n = intString(value)
	return nil
}

// int32Value applies System.Int32 JSON bounds and rejects fractional numbers.
type int32Value int32

func (n *int32Value) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		return fmt.Errorf("ffprobe Int32: null")
	}
	var value int32
	if err := json.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("ffprobe Int32: %w", err)
	}
	*n = int32Value(value)
	return nil
}

// stringScalar accepts the quoted and numeric representations ffprobe uses for
// nominal string fields such as nal_length_size.
type stringScalar string

func (s *stringScalar) UnmarshalJSON(data []byte) error {
	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		*s = stringScalar(text)
		return nil
	}
	var number json.Number
	if err := json.Unmarshal(data, &number); err != nil {
		return fmt.Errorf("ffprobe string: %w", err)
	}
	*s = stringScalar(number.String())
	return nil
}

// tags mirrors the upstream Dictionary<string, string?> wire contract. JSON
// numbers are rejected rather than coerced into metadata strings.
type tags map[string]*string

// chapterTags retains System.Text.Json's case-sensitive dictionary behavior;
// chapter metadata is not converted through FFProbeHelpers' case-folding path.
type chapterTags map[string]*string

// Result is the direct, loss-minimizing ffprobe JSON wire shape. It is kept

func (t *tags) UnmarshalJSON(data []byte) error {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		*t = nil
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("ffprobe tags: %w", err)
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '{' {
		return fmt.Errorf("ffprobe tags: expected object")
	}
	exact := make(tags)
	order := make([]string, 0)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return fmt.Errorf("ffprobe tag key: %w", err)
		}
		key, ok := token.(string)
		if !ok {
			return fmt.Errorf("ffprobe tag key is not a string")
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return fmt.Errorf("ffprobe tag %q: %w", key, err)
		}
		var value *string
		if string(raw) != "null" {
			var text string
			if err := json.Unmarshal(raw, &text); err != nil {
				return fmt.Errorf("ffprobe tag %q: %w", key, err)
			}
			value = &text
		}
		if _, exists := exact[key]; !exists {
			order = append(order, key)
		}
		exact[key] = value
	}
	if _, err := decoder.Token(); err != nil {
		return fmt.Errorf("ffprobe tags: %w", err)
	}
	result := make(tags)
	for _, key := range order {
		duplicate := false
		for existing := range result {
			if strings.EqualFold(existing, key) {
				duplicate = true
				break
			}
		}
		if !duplicate {
			result[key] = exact[key]
		}
	}
	*t = result
	return nil
}

// private to the probe boundary; Normalize turns it into playback domain facts.
type Result struct {
	Streams    []stream         `json:"streams"`
	Format     *format          `json:"format"`
	Chapters   []chapter        `json:"chapters"`
	Frames     []frame          `json:"frames"`
	MP3Gapless *mp3GaplessFacts `json:"deadfin_mp3_gapless"`
}

type stream struct {
	Index              int32Value            `json:"index"`
	Profile            string                `json:"profile"`
	CodecName          string                `json:"codec_name"`
	CodecType          string                `json:"codec_type"`
	ExtraData          string                `json:"extradata"`
	SampleRate         string                `json:"sample_rate"`
	Channels           *int32Value           `json:"channels"`
	ChannelLayout      string                `json:"channel_layout"`
	AverageFrameRate   string                `json:"avg_frame_rate"`
	Duration           string                `json:"duration"`
	BitRate            string                `json:"bit_rate"`
	Width              *int32Value           `json:"width"`
	Height             *int32Value           `json:"height"`
	Refs               int32Value            `json:"refs"`
	DisplayAspectRatio string                `json:"display_aspect_ratio"`
	Tags               tags                  `json:"tags"`
	BitsPerSample      intString             `json:"bits_per_sample"`
	BitsPerRawSample   intString             `json:"bits_per_raw_sample"`
	RealFrameRate      string                `json:"r_frame_rate"`
	SampleAspectRatio  string                `json:"sample_aspect_ratio"`
	PixelFormat        string                `json:"pix_fmt"`
	Level              *int32Value           `json:"level"`
	TimeBase           string                `json:"time_base"`
	CodecTimeBase      string                `json:"codec_time_base"`
	CodecTagString     string                `json:"codec_tag_string?"`
	IsAVC              *boolString           `json:"is_avc"`
	NALLengthSize      stringScalar          `json:"nal_length_size"`
	FieldOrder         string                `json:"field_order"`
	Disposition        map[string]int32Value `json:"disposition"`
	ColorRange         string                `json:"color_range"`
	ColorSpace         string                `json:"color_space"`
	ColorTransfer      string                `json:"color_transfer"`
	ColorPrimaries     string                `json:"color_primaries"`
	SideData           []sideData            `json:"side_data_list"`
}

type format struct {
	FormatName string `json:"format_name"`
	Duration   string `json:"duration"`
	Size       string `json:"size"`
	BitRate    string `json:"bit_rate"`
	Tags       tags   `json:"tags"`
}

type frame struct {
	StreamIndex *int32Value `json:"stream_index"`
	SideData    []sideData  `json:"side_data_list"`
}

type sideData struct {
	Type                      string      `json:"side_data_type"`
	DVVersionMajor            *int32Value `json:"dv_version_major"`
	DVVersionMinor            *int32Value `json:"dv_version_minor"`
	DVProfile                 *int32Value `json:"dv_profile"`
	DVLevel                   *int32Value `json:"dv_level"`
	RPUPresentFlag            *int32Value `json:"rpu_present_flag"`
	ELPresentFlag             *int32Value `json:"el_present_flag"`
	BLPresentFlag             *int32Value `json:"bl_present_flag"`
	DVBLSignalCompatibilityID *int32Value `json:"dv_bl_signal_compatibility_id"`
	Rotation                  *int32Value `json:"rotation"`
}

type mp3GaplessFacts struct {
	UntrimmedTicks int64 `json:"untrimmed_ticks"`
	SkipSamples    int64 `json:"skip_samples"`
	DiscardPadding int64 `json:"discard_padding"`
	SampleRate     int64 `json:"sample_rate"`
}

type chapter struct {
	StartTime string      `json:"start_time"`
	Tags      chapterTags `json:"tags"`
}

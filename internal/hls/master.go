// SPDX-License-Identifier: GPL-2.0-only

package hls

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	// 8K UHD is 7,680×4,320 (33,177,600 pixels). These limits retain
	// supported 8K renditions without allowing overflow-prone dimensions.
	maxMasterDimension = 8_192
	maxMasterPixels    = 32 << 20
)

type masterPlaylistWriter interface {
	WriteString(string)
	AppendByte(byte)
}

type masterPlaylistBuilder struct {
	builder strings.Builder
}

func (writer *masterPlaylistBuilder) Grow(size int) {
	writer.builder.Grow(size)
}

func (writer *masterPlaylistBuilder) WriteString(value string) {
	_, _ = writer.builder.WriteString(value)
}

func (writer *masterPlaylistBuilder) AppendByte(value byte) {
	_ = writer.builder.WriteByte(value)
}

func (writer *masterPlaylistBuilder) String() string {
	return writer.builder.String()
}

type masterPlaylistCounter struct {
	counter playlistSizeCounter
}

func (writer *masterPlaylistCounter) WriteString(value string) {
	writer.counter.addString(value)
}

func (writer *masterPlaylistCounter) AppendByte(byte) {
	writer.counter.add(1)
}

func (writer *masterPlaylistCounter) result() (int, error) {
	return writer.counter.result()
}

// VideoRange identifies the display range represented by a video rendition.
type VideoRange string

const (
	VideoRangeUnknown VideoRange = ""
	VideoRangeSDR     VideoRange = "SDR"
	VideoRangeHDR     VideoRange = "HDR"
	VideoRangeHLG     VideoRange = "HLG"
)

// DolbyVision describes dynamic metadata accompanying a compatible base video
// stream. BaseLayer identifies the range of that base stream. Direct is set
// only when the requesting client explicitly declared Dolby Vision support.
type DolbyVision struct {
	Profile   int
	Level     int
	BaseLayer VideoRange
	Direct    bool
}

// MasterVideo contains source video facts for one master-playlist variant.
type MasterVideo struct {
	Codec       string
	Profile     string
	Level       int
	HighTier    bool
	BitDepth    int
	PixelFormat string
	Range       VideoRange
	HDR10Plus   bool
	DolbyVision *DolbyVision
}

// MasterAudio contains source audio facts for one master-playlist variant.
type MasterAudio struct {
	Codec   string
	Profile string
}

// MasterVariant defines one playable media rendition.
type MasterVariant struct {
	URI           string
	Video         MasterVideo
	Audio         MasterAudio
	VideoBitrate  int
	AudioBitrate  int
	Width         int
	Height        int
	FrameRate     float64
	SubtitleGroup string
	SDRCompanion  *MasterVideo
}

// SubtitleRendition defines a subtitle media line emitted before variants.
type SubtitleRendition struct {
	GroupID  string
	Name     string
	URI      string
	Language string
	Default  bool
	Forced   bool
}

// TrickplayRendition defines an image-stream playlist emitted after media
// variants.
type TrickplayRendition struct {
	URI       string
	Bandwidth int
	Width     int
	Height    int
}

// MasterRequest collects the source facts needed to serialize a master HLS
// playlist. URIs are already encoded relative URIs and are never parsed or
// normalized here.
type MasterRequest struct {
	Variants  []MasterVariant
	Subtitles []SubtitleRendition
	Trickplay []TrickplayRendition
}

// BuildMasterPlaylist renders a master playlist using Jellyfin's established
// byte order for attributes and rendition classes.
func BuildMasterPlaylist(request MasterRequest) (string, error) {
	if err := validateMasterRequest(request); err != nil {
		return "", err
	}
	size, err := masterPlaylistOutputSize(request)
	if err != nil {
		return "", err
	}

	var builder masterPlaylistBuilder
	builder.Grow(size)
	builder.WriteString("#EXTM3U\n")
	for _, subtitle := range request.Subtitles {
		appendSubtitle(&builder, subtitle)
	}
	for _, variant := range request.Variants {
		if hasDirectDolbyVision(variant.Video) {
			appendVariant(&builder, variant, variant.Video, variant.URI, true)
		}
		appendVariant(&builder, variant, variant.Video, variant.URI, false)
		if variant.SDRCompanion != nil {
			appendCompanionVariant(&builder, variant, *variant.SDRCompanion)
		}
	}
	for _, trickplay := range request.Trickplay {
		appendTrickplay(&builder, trickplay)
	}
	return builder.String(), nil
}

func masterPlaylistOutputSize(request MasterRequest) (int, error) {
	if err := checkMasterPlaylistEntries(request); err != nil {
		return 0, err
	}

	var counter masterPlaylistCounter
	counter.WriteString("#EXTM3U\n")
	for _, subtitle := range request.Subtitles {
		appendSubtitle(&counter, subtitle)
	}
	for _, variant := range request.Variants {
		if hasDirectDolbyVision(variant.Video) {
			appendVariant(&counter, variant, variant.Video, variant.URI, true)
		}
		appendVariant(&counter, variant, variant.Video, variant.URI, false)
		if variant.SDRCompanion != nil {
			appendCompanionVariant(&counter, variant, *variant.SDRCompanion)
		}
	}
	for _, trickplay := range request.Trickplay {
		appendTrickplay(&counter, trickplay)
	}
	return counter.result()
}

func checkMasterPlaylistEntries(request MasterRequest) error {
	entries := int64(len(request.Subtitles))
	if err := checkPlaylistSegmentCount(entries); err != nil {
		return err
	}
	for _, variant := range request.Variants {
		variantEntries := int64(1)
		if hasDirectDolbyVision(variant.Video) {
			variantEntries++
		}
		if variant.SDRCompanion != nil {
			variantEntries++
		}
		if variantEntries > maxPlaylistSegmentCount-entries {
			return errPlaylistOutputLimit
		}
		entries += variantEntries
	}
	if int64(len(request.Trickplay)) > maxPlaylistSegmentCount-entries {
		return errPlaylistOutputLimit
	}
	return nil
}

func appendSubtitle(writer masterPlaylistWriter, subtitle SubtitleRendition) {
	writer.WriteString("#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID=\"")
	writer.WriteString(subtitle.GroupID)
	writer.WriteString("\",NAME=\"")
	writer.WriteString(subtitle.Name)
	writer.WriteString("\",DEFAULT=")
	writer.WriteString(yesNo(subtitle.Default))
	writer.WriteString(",FORCED=")
	writer.WriteString(yesNo(subtitle.Forced))
	writer.WriteString(",AUTOSELECT=YES,URI=\"")
	writer.WriteString(subtitle.URI)
	writer.WriteString("\",LANGUAGE=\"")
	writer.WriteString(subtitle.Language)
	writer.WriteString("\"\n")
}

func appendVariant(writer masterPlaylistWriter, source MasterVariant, video MasterVideo, uri string, directDolbyVision bool) {
	appendVariantAttributes(writer, source, video, directDolbyVision)
	writer.WriteString(uri)
	writer.AppendByte('\n')
}

func appendCompanionVariant(writer masterPlaylistWriter, source MasterVariant, video MasterVideo) {
	appendVariantAttributes(writer, source, video, false)
	appendCompanionURI(writer, source.URI, video.Codec)
	writer.AppendByte('\n')
}

func appendVariantAttributes(writer masterPlaylistWriter, source MasterVariant, video MasterVideo, directDolbyVision bool) {
	bandwidth := source.VideoBitrate + source.AudioBitrate
	writer.WriteString("#EXT-X-STREAM-INF:BANDWIDTH=")
	writer.WriteString(strconv.Itoa(bandwidth))
	writer.WriteString(",AVERAGE-BANDWIDTH=")
	writer.WriteString(strconv.Itoa(bandwidth))
	appendVideoRange(writer, video.Range)
	appendCodecs(writer, source, video, source.Audio, directDolbyVision)
	appendSupplementalCodecs(writer, source, video)
	if source.Width != 0 && source.Height != 0 {
		writer.WriteString(",RESOLUTION=")
		writer.WriteString(strconv.Itoa(source.Width))
		writer.AppendByte('x')
		writer.WriteString(strconv.Itoa(source.Height))
	}
	if source.FrameRate != 0 {
		writer.WriteString(",FRAME-RATE=")
		writer.WriteString(formatFrameRate(source.FrameRate))
	}
	if source.SubtitleGroup != "" {
		writer.WriteString(",SUBTITLES=\"")
		writer.WriteString(source.SubtitleGroup)
		writer.AppendByte('"')
	}
	writer.AppendByte('\n')
}

func appendTrickplay(writer masterPlaylistWriter, trickplay TrickplayRendition) {
	writer.WriteString("#EXT-X-IMAGE-STREAM-INF:BANDWIDTH=")
	writer.WriteString(strconv.Itoa(trickplay.Bandwidth))
	writer.WriteString(",RESOLUTION=")
	writer.WriteString(strconv.Itoa(trickplay.Width))
	writer.AppendByte('x')
	writer.WriteString(strconv.Itoa(trickplay.Height))
	writer.WriteString(",CODECS=\"jpeg\",URI=\"")
	writer.WriteString(trickplay.URI)
	writer.WriteString("\"\n")
}

func appendVideoRange(writer masterPlaylistWriter, videoRange VideoRange) {
	switch videoRange {
	case VideoRangeSDR:
		writer.WriteString(",VIDEO-RANGE=SDR")
	case VideoRangeHLG:
		writer.WriteString(",VIDEO-RANGE=HLG")
	case VideoRangeHDR:
		writer.WriteString(",VIDEO-RANGE=PQ")
	}
}

func appendCodecs(writer masterPlaylistWriter, source MasterVariant, video MasterVideo, audio MasterAudio, directDolbyVision bool) {
	videoCodec := masterVideoCodecString(source, video, directDolbyVision)
	audioCodec := AudioCodecString(AudioCodec(audio))
	if videoCodec == "" && audioCodec == "" {
		return
	}
	writer.WriteString(",CODECS=\"")
	writer.WriteString(videoCodec)
	if videoCodec != "" && audioCodec != "" {
		writer.AppendByte(',')
	}
	writer.WriteString(audioCodec)
	writer.AppendByte('"')
}

func masterVideoCodecString(source MasterVariant, video MasterVideo, directDolbyVision bool) string {
	if video.Level <= 0 && !strings.EqualFold(video.Codec, "vp9") {
		return ""
	}
	codec := VideoCodec{
		Codec: video.Codec, Profile: video.Profile, Level: video.Level, HighTier: video.HighTier, BitDepth: video.BitDepth,
		Width: source.Width, Height: source.Height, PixelFormat: video.PixelFormat, FrameRate: source.FrameRate,
	}
	if directDolbyVision {
		codec.DolbyVisionProfile = video.DolbyVision.Profile
		codec.DolbyVisionLevel = video.DolbyVision.Level
	}
	return VideoCodecString(codec)
}

func appendSupplementalCodecs(writer masterPlaylistWriter, source MasterVariant, video MasterVideo) {
	dovi := video.DolbyVision
	if dovi != nil && dovi.Profile > 0 && dovi.Level > 0 {
		baseLayer := ""
		switch dovi.BaseLayer {
		case VideoRangeHDR:
			baseLayer = "db1p"
		case VideoRangeHLG:
			baseLayer = "db4h"
		}
		if baseLayer != "" {
			writer.WriteString(",SUPPLEMENTAL-CODECS=\"")
			writer.WriteString(doviCodecString(dovi.Profile, dovi.Level, video.Codec))
			writer.AppendByte('/')
			writer.WriteString(baseLayer)
			writer.AppendByte('"')
		}
		return
	}
	if !video.HDR10Plus {
		return
	}
	baseCodec := masterVideoCodecString(source, video, false)
	if baseCodec == "" {
		return
	}
	writer.WriteString(",SUPPLEMENTAL-CODECS=\"")
	writer.WriteString(baseCodec)
	writer.WriteString("/cdm4\"")
}

func hasDirectDolbyVision(video MasterVideo) bool {
	dovi := video.DolbyVision
	return video.Level > 0 && dovi != nil && dovi.Direct && dovi.Profile > 0 && dovi.Level > 0 && dovi.BaseLayer == VideoRangeUnknown
}

func appendCompanionURI(writer masterPlaylistWriter, uri, codec string) {
	fragment := ""
	if fragmentIndex := strings.IndexByte(uri, '#'); fragmentIndex >= 0 {
		fragment = uri[fragmentIndex:]
		uri = uri[:fragmentIndex]
	}

	base, query, hasQuery := uri, "", false
	if queryIndex := strings.IndexByte(uri, '?'); queryIndex >= 0 {
		base, query, hasQuery = uri[:queryIndex], uri[queryIndex+1:], true
	}

	writer.WriteString(base)
	writer.AppendByte('?')
	wrotePart := false
	if hasQuery && query != "" {
		for start := 0; start <= len(query); {
			end := strings.IndexByte(query[start:], '&')
			if end < 0 {
				end = len(query)
			} else {
				end += start
			}
			part := query[start:end]
			rawKey, _, _ := strings.Cut(part, "=")
			if !isCompanionOverrideKey(rawKey) {
				appendQueryPart(writer, &wrotePart, part)
			}
			if end == len(query) {
				break
			}
			start = end + 1
		}
	}
	appendQueryKeyValue(writer, &wrotePart, "VideoCodec=", codec)
	appendQueryPart(writer, &wrotePart, "AllowVideoStreamCopy=false")
	writer.WriteString(fragment)
}

func appendQueryPart(writer masterPlaylistWriter, wrotePart *bool, part string) {
	if *wrotePart {
		writer.AppendByte('&')
	}
	writer.WriteString(part)
	*wrotePart = true
}

func appendQueryKeyValue(writer masterPlaylistWriter, wrotePart *bool, key, value string) {
	if *wrotePart {
		writer.AppendByte('&')
	}
	writer.WriteString(key)
	writer.WriteString(value)
	*wrotePart = true
}

func isCompanionOverrideKey(raw string) bool {
	var decoded [len("AllowVideoStreamCopy")]byte
	length := 0
	for index := 0; index < len(raw); {
		var value byte
		switch raw[index] {
		case '+':
			value = ' '
			index++
		case '%':
			if index+2 >= len(raw) {
				return false
			}
			high, highOK := hexadecimalNibble(raw[index+1])
			low, lowOK := hexadecimalNibble(raw[index+2])
			if !highOK || !lowOK {
				return false
			}
			value = high<<4 | low
			index += 3
		default:
			value = raw[index]
			index++
		}
		if length == len(decoded) {
			return false
		}
		decoded[length] = value
		length++
	}
	key := decoded[:length]
	return asciiEqualFold(key, "VideoCodec") || asciiEqualFold(key, "AllowVideoStreamCopy")
}

func hexadecimalNibble(value byte) (byte, bool) {
	switch {
	case value >= '0' && value <= '9':
		return value - '0', true
	case value >= 'a' && value <= 'f':
		return value - 'a' + 10, true
	case value >= 'A' && value <= 'F':
		return value - 'A' + 10, true
	default:
		return 0, false
	}
}

func asciiEqualFold(value []byte, expected string) bool {
	if len(value) != len(expected) {
		return false
	}
	for index, byteValue := range value {
		expectedByte := expected[index]
		if byteValue == expectedByte {
			continue
		}
		if byteValue >= 'A' && byteValue <= 'Z' {
			byteValue += 'a' - 'A'
		}
		if expectedByte >= 'A' && expectedByte <= 'Z' {
			expectedByte += 'a' - 'A'
		}
		if byteValue != expectedByte {
			return false
		}
	}
	return true
}

func formatFrameRate(frameRate float64) string {
	frameRate = math.Round(frameRate*1000) / 1000
	return strconv.FormatFloat(frameRate, 'f', -1, 64)
}

func yesNo(value bool) string {
	if value {
		return "YES"
	}
	return "NO"
}

func validateMasterRequest(request MasterRequest) error {
	for i, subtitle := range request.Subtitles {
		if subtitle.GroupID == "" || subtitle.Name == "" || subtitle.URI == "" {
			return fmt.Errorf("invalid subtitle rendition %d", i)
		}
		if err := validateAttribute(subtitle.GroupID); err != nil {
			return fmt.Errorf("invalid subtitle rendition %d: %w", i, err)
		}
		if err := validateAttribute(subtitle.Name); err != nil {
			return fmt.Errorf("invalid subtitle rendition %d: %w", i, err)
		}
		if err := validateAttribute(subtitle.URI); err != nil {
			return fmt.Errorf("invalid subtitle rendition %d: %w", i, err)
		}
		if err := validateAttribute(subtitle.Language); err != nil {
			return fmt.Errorf("invalid subtitle rendition %d: %w", i, err)
		}
	}
	for i, variant := range request.Variants {
		if variant.URI == "" || variant.VideoBitrate < 0 || variant.AudioBitrate < 0 || variant.Width < 0 || variant.Height < 0 || variant.FrameRate < 0 || math.IsNaN(variant.FrameRate) || math.IsInf(variant.FrameRate, 0) {
			return fmt.Errorf("invalid master variant %d", i)
		}
		if (variant.Width == 0) != (variant.Height == 0) {
			return fmt.Errorf("invalid master variant %d", i)
		}
		if err := validateMasterDimensions(variant.Width, variant.Height); err != nil {
			return fmt.Errorf("invalid master variant %d: %w", i, err)
		}
		if err := validateLine(variant.URI); err != nil {
			return fmt.Errorf("invalid master variant %d: %w", i, err)
		}
		if err := validateAttribute(variant.SubtitleGroup); err != nil {
			return fmt.Errorf("invalid master variant %d: %w", i, err)
		}
		if variant.SDRCompanion != nil && variant.SDRCompanion.Codec == "" {
			return fmt.Errorf("invalid master variant %d SDR companion", i)
		}
	}
	for i, trickplay := range request.Trickplay {
		if trickplay.URI == "" || trickplay.Bandwidth < 0 || trickplay.Width <= 0 || trickplay.Height <= 0 {
			return fmt.Errorf("invalid trickplay rendition %d", i)
		}
		if err := validateMasterDimensions(trickplay.Width, trickplay.Height); err != nil {
			return fmt.Errorf("invalid trickplay rendition %d: %w", i, err)
		}
		if err := validateAttribute(trickplay.URI); err != nil {
			return fmt.Errorf("invalid trickplay rendition %d: %w", i, err)
		}
	}
	return nil
}

func validateMasterDimensions(width, height int) error {
	if width == 0 && height == 0 {
		return nil
	}
	if width <= 0 || height <= 0 || width > maxMasterDimension || height > maxMasterDimension || width > maxMasterPixels/height {
		return fmt.Errorf("dimensions exceed 8192 pixels per axis or 33554432 pixels total")
	}
	return nil
}

func validateAttribute(value string) error {
	return validateMasterText(value, true)
}

func validateLine(value string) error {
	return validateMasterText(value, false)
}

func validateMasterText(value string, quoted bool) error {
	if !utf8.ValidString(value) {
		return fmt.Errorf("contains a control character or quote")
	}
	for _, character := range value {
		if character <= 0x1f || character == 0x7f || (character >= 0x80 && character <= 0x9f) || (quoted && character == '"') {
			return fmt.Errorf("contains a control character or quote")
		}
	}
	return nil
}

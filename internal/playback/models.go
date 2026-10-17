// SPDX-License-Identifier: GPL-2.0-only

package playback

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/deadfin-org/deadfin/internal/jsonnum"
)

// Shared DLNA/media model declarations. Computed MediaStream behavior lives in
// media_stream.go; this file owns only fields, enums, and profile helpers.
type DlnaProfileType int

const (
	DlnaAudio DlnaProfileType = iota
	DlnaVideo
	DlnaPhoto
	DlnaSubtitle
	DlnaLyric
)

type CodecType int

const (
	CodecVideo CodecType = iota
	CodecVideoAudio
	CodecAudio
)

type EncodingContext int

const (
	ContextStreaming EncodingContext = iota
	ContextStatic
)

type PlayMethod int

const (
	PlayMethodTranscode PlayMethod = iota
	PlayMethodDirectStream
	PlayMethodDirectPlay
)

type MediaProtocol int

const (
	MediaProtocolFile MediaProtocol = iota
	MediaProtocolHTTP
	MediaProtocolRTMP
	MediaProtocolRTSP
	MediaProtocolUDP
	MediaProtocolRTP
	MediaProtocolFTP
)

type MediaStreamProtocol int

const (
	StreamProtocolHTTP MediaStreamProtocol = iota
	StreamProtocolHLS
)

type StreamType int

const (
	StreamTypeAudio StreamType = iota
	StreamTypeVideo
	StreamTypeSubtitle
	StreamTypeEmbeddedImage
	StreamTypeData
	StreamTypeLyric
)

// MediaStreamType is the name used by the pinned StreamBuilder source.
type MediaStreamType = StreamType

const (
	MediaStreamAudio    = StreamTypeAudio
	MediaStreamVideo    = StreamTypeVideo
	MediaStreamSubtitle = StreamTypeSubtitle
)

type VideoRange int

// Values are prefixed to coexist with the already-public VideoRangeType
// constants used by ConditionProcessor.
const (
	MediaVideoRangeUnknown VideoRange = iota
	MediaVideoRangeSDR
	MediaVideoRangeHDR
)

type MediaSourceType int

const (
	MediaSourceDefault MediaSourceType = iota
	MediaSourceGrouping
	MediaSourcePlaceholder
)

type IsoType int

const (
	IsoDVD IsoType = iota
	IsoBluRay
)

type Video3DFormat int

const (
	Video3DHalfSideBySide Video3DFormat = iota
	Video3DFullSideBySide
	Video3DFullTopAndBottom
	Video3DHalfTopAndBottom
	Video3DMVC
)

type AudioSpatialFormat int

const (
	AudioSpatialNone AudioSpatialFormat = iota
	AudioSpatialDolbyAtmos
	AudioSpatialDTSX
)

type SubtitleDeliveryMethod int

const (
	SubtitleEncode SubtitleDeliveryMethod = iota
	SubtitleEmbed
	SubtitleExternal
	SubtitleHLS
	SubtitleDrop
)

type VideoType int

const (
	VideoTypeVideo VideoType = iota
	VideoTypeIso
	VideoTypeDVD
	VideoTypeBluRay
	VideoTypeHDVD
	VideoTypeUnknown
)

type AudioIndexSource uint8

const (
	AudioIndexNone     AudioIndexSource = 0
	AudioIndexLanguage AudioIndexSource = 1 << 0
	AudioIndexDefault  AudioIndexSource = 1 << 1
	AudioIndexUser     AudioIndexSource = 1 << 2
)

type TranscodeSeekInfo int

const (
	SeekAuto TranscodeSeekInfo = iota
	SeekBytes
	SeekTime
)

type TranscodeReason uint32

const (
	ReasonContainerNotSupported        TranscodeReason = 1 << 0
	ReasonVideoCodecNotSupported       TranscodeReason = 1 << 1
	ReasonAudioCodecNotSupported       TranscodeReason = 1 << 2
	ReasonSubtitleCodecNotSupported    TranscodeReason = 1 << 3
	ReasonAudioIsExternal              TranscodeReason = 1 << 4
	ReasonSecondaryAudioNotSupported   TranscodeReason = 1 << 5
	ReasonVideoProfileNotSupported     TranscodeReason = 1 << 6
	ReasonVideoLevelNotSupported       TranscodeReason = 1 << 7
	ReasonVideoResolutionNotSupported  TranscodeReason = 1 << 8
	ReasonVideoBitDepthNotSupported    TranscodeReason = 1 << 9
	ReasonVideoFramerateNotSupported   TranscodeReason = 1 << 10
	ReasonRefFramesNotSupported        TranscodeReason = 1 << 11
	ReasonAnamorphicVideoNotSupported  TranscodeReason = 1 << 12
	ReasonInterlacedVideoNotSupported  TranscodeReason = 1 << 13
	ReasonAudioChannelsNotSupported    TranscodeReason = 1 << 14
	ReasonAudioProfileNotSupported     TranscodeReason = 1 << 15
	ReasonAudioSampleRateNotSupported  TranscodeReason = 1 << 16
	ReasonAudioBitDepthNotSupported    TranscodeReason = 1 << 17
	ReasonContainerBitrateExceedsLimit TranscodeReason = 1 << 18
	ReasonVideoBitrateNotSupported     TranscodeReason = 1 << 19
	ReasonAudioBitrateNotSupported     TranscodeReason = 1 << 20
	ReasonUnknownVideoStreamInfo       TranscodeReason = 1 << 21
	ReasonUnknownAudioStreamInfo       TranscodeReason = 1 << 22
	ReasonDirectPlayError              TranscodeReason = 1 << 23
	ReasonVideoRangeTypeNotSupported   TranscodeReason = 1 << 24
	ReasonVideoCodecTagNotSupported    TranscodeReason = 1 << 25
	ReasonStreamCountExceedsLimit      TranscodeReason = 1 << 26
	ReasonVideoRotationNotSupported    TranscodeReason = 1 << 27
)

const (
	ContainerReasons    = ReasonContainerNotSupported | ReasonContainerBitrateExceedsLimit
	AudioCodecReasons   = ReasonAudioBitrateNotSupported | ReasonAudioChannelsNotSupported | ReasonAudioProfileNotSupported | ReasonAudioSampleRateNotSupported | ReasonSecondaryAudioNotSupported | ReasonAudioBitDepthNotSupported | ReasonAudioIsExternal
	AudioReasons        = ReasonAudioCodecNotSupported | AudioCodecReasons
	VideoCodecReasons   = ReasonVideoResolutionNotSupported | ReasonAnamorphicVideoNotSupported | ReasonInterlacedVideoNotSupported | ReasonVideoBitDepthNotSupported | ReasonVideoBitrateNotSupported | ReasonVideoFramerateNotSupported | ReasonVideoLevelNotSupported | ReasonRefFramesNotSupported | ReasonVideoRangeTypeNotSupported | ReasonVideoProfileNotSupported | ReasonVideoRotationNotSupported
	VideoReasons        = ReasonVideoCodecNotSupported | VideoCodecReasons
	DirectStreamReasons = AudioReasons | ReasonContainerNotSupported | ReasonVideoCodecTagNotSupported
)

var enumNames = map[any]map[string]int{
	DlnaProfileType(0):        {"audio": 0, "video": 1, "photo": 2, "subtitle": 3, "lyric": 4},
	CodecType(0):              {"video": 0, "videoaudio": 1, "audio": 2},
	EncodingContext(0):        {"streaming": 0, "static": 1},
	PlayMethod(0):             {"transcode": 0, "directstream": 1, "directplay": 2},
	MediaProtocol(0):          {"file": 0, "http": 1, "rtmp": 2, "rtsp": 3, "udp": 4, "rtp": 5, "ftp": 6},
	MediaStreamProtocol(0):    {"http": 0, "hls": 1},
	StreamType(0):             {"audio": 0, "video": 1, "subtitle": 2, "embeddedimage": 3, "data": 4, "lyric": 5},
	SubtitleDeliveryMethod(0): {"encode": 0, "embed": 1, "external": 2, "hls": 3, "drop": 4},
	VideoType(0):              {"videofile": 0, "iso": 1, "dvd": 2, "bluray": 3, "hdvd": 4, "unknown": 5},
	MediaSourceType(0):        {"default": 0, "grouping": 1, "placeholder": 2},
	IsoType(0):                {"dvd": 0, "bluray": 1},
	Video3DFormat(0):          {"halfsidebyside": 0, "fullsidebyside": 1, "fulltopandbottom": 2, "halftopandbottom": 3, "mvc": 4},
	AudioSpatialFormat(0):     {"none": 0, "dolbyatmos": 1, "dtsx": 2},
	TranscodeSeekInfo(0):      {"auto": 0, "bytes": 1, "time": 2},
}

func enumJSON(data []byte, names map[string]int) (int, error) {
	var name string
	if err := json.Unmarshal(data, &name); err == nil {
		if name == "" {
			return 0, nil
		}
		if value, ok := names[strings.ToLower(name)]; ok {
			return value, nil
		}
		if value, err := strconv.Atoi(name); err == nil {
			return value, nil
		}
		return 0, fmt.Errorf("unknown enum value %q", name)
	}
	var value int
	if err := json.Unmarshal(data, &value); err != nil {
		return 0, err
	}
	return value, nil
}

func enumUnmarshal(data []byte, key any, dst *int) error {
	value, err := enumJSON(data, enumNames[key])
	if err == nil {
		*dst = value
	}
	return err
}

func (v *DlnaProfileType) UnmarshalJSON(b []byte) error {
	var x int
	err := enumUnmarshal(b, DlnaProfileType(0), &x)
	*v = DlnaProfileType(x)
	return err
}

func (v *CodecType) UnmarshalJSON(b []byte) error {
	var x int
	err := enumUnmarshal(b, CodecType(0), &x)
	*v = CodecType(x)
	return err
}

func (v *EncodingContext) UnmarshalJSON(b []byte) error {
	var x int
	err := enumUnmarshal(b, EncodingContext(0), &x)
	*v = EncodingContext(x)
	return err
}

func (v *PlayMethod) UnmarshalJSON(b []byte) error {
	var x int
	err := enumUnmarshal(b, PlayMethod(0), &x)
	*v = PlayMethod(x)
	return err
}

func (v *MediaProtocol) UnmarshalJSON(b []byte) error {
	var x int
	err := enumUnmarshal(b, MediaProtocol(0), &x)
	*v = MediaProtocol(x)
	return err
}

func (v *MediaStreamProtocol) UnmarshalJSON(b []byte) error {
	var x int
	err := enumUnmarshal(b, MediaStreamProtocol(0), &x)
	*v = MediaStreamProtocol(x)
	return err
}

func (v *StreamType) UnmarshalJSON(b []byte) error {
	var x int
	err := enumUnmarshal(b, StreamType(0), &x)
	*v = StreamType(x)
	return err
}

func (v *SubtitleDeliveryMethod) UnmarshalJSON(b []byte) error {
	var x int
	err := enumUnmarshal(b, SubtitleDeliveryMethod(0), &x)
	*v = SubtitleDeliveryMethod(x)
	return err
}

func (v *VideoType) UnmarshalJSON(b []byte) error {
	var x int
	err := enumUnmarshal(b, VideoType(0), &x)
	*v = VideoType(x)
	return err
}

func (v *TranscodeSeekInfo) UnmarshalJSON(b []byte) error {
	var x int
	err := enumUnmarshal(b, TranscodeSeekInfo(0), &x)
	*v = TranscodeSeekInfo(x)
	return err
}

func (v *MediaSourceType) UnmarshalJSON(b []byte) error {
	var x int
	err := enumUnmarshal(b, MediaSourceType(0), &x)
	*v = MediaSourceType(x)
	return err
}

func (v *IsoType) UnmarshalJSON(b []byte) error {
	var x int
	err := enumUnmarshal(b, IsoType(0), &x)
	*v = IsoType(x)
	return err
}

func (v *Video3DFormat) UnmarshalJSON(b []byte) error {
	var x int
	err := enumUnmarshal(b, Video3DFormat(0), &x)
	*v = Video3DFormat(x)
	return err
}

func (v *AudioSpatialFormat) UnmarshalJSON(b []byte) error {
	var x int
	err := enumUnmarshal(b, AudioSpatialFormat(0), &x)
	*v = AudioSpatialFormat(x)
	return err
}

type DirectPlayProfile struct {
	Container  string          `json:"Container"`
	AudioCodec string          `json:"AudioCodec"`
	VideoCodec string          `json:"VideoCodec"`
	Type       DlnaProfileType `json:"Type"`

	audioCodecSet bool
	videoCodecSet bool
}

func (p DirectPlayProfile) SupportsContainer(container string) bool {
	return ContainsContainer(p.Container, container)
}

func (p DirectPlayProfile) SupportsVideoCodec(codec string) bool {
	return p.Type == DlnaVideo && ContainsContainer(p.VideoCodec, codec)
}

func (p DirectPlayProfile) SupportsAudioCodec(codec string) bool {
	return (p.Type == DlnaAudio || p.Type == DlnaVideo) && ContainsContainer(p.AudioCodec, codec)
}

type CodecProfile struct {
	Type            CodecType          `json:"Type"`
	Conditions      []ProfileCondition `json:"Conditions"`
	ApplyConditions []ProfileCondition `json:"ApplyConditions"`
	Codec           string             `json:"Codec"`
	Container       string             `json:"Container"`
	SubContainer    string             `json:"SubContainer"`

	codecSet        bool
	containerSet    bool
	subContainerSet bool
}

func (p CodecProfile) ContainsAnyCodec(codec, container string, useSubContainer bool) bool {
	check := p.Container
	if useSubContainer && strings.EqualFold(check, "hls") {
		check = p.SubContainer
	}
	return ContainsContainer(check, container) && ContainsContainer(p.Codec, codec)
}

func (p CodecProfile) ContainsAnyCodecs(codecs []string, container string, useSubContainer bool) bool {
	for _, codec := range codecs {
		if p.ContainsAnyCodec(codec, container, useSubContainer) {
			return true
		}
	}
	return false
}

type ContainerProfile struct {
	Type         DlnaProfileType    `json:"Type"`
	Conditions   []ProfileCondition `json:"Conditions"`
	Container    string             `json:"Container"`
	SubContainer string             `json:"SubContainer"`

	containerSet    bool
	subContainerSet bool
}

func (p ContainerProfile) ContainsContainer(container string, useSubContainer bool) bool {
	check := p.Container
	if useSubContainer && strings.EqualFold(check, "hls") {
		check = p.SubContainer
	}
	return ContainsContainer(check, container)
}

type SubtitleProfile struct {
	Format    string                 `json:"Format"`
	Method    SubtitleDeliveryMethod `json:"Method"`
	DidlMode  string                 `json:"DidlMode,omitempty"`
	Language  string                 `json:"Language,omitempty"`
	Container string                 `json:"Container,omitempty"`
}

func (p SubtitleProfile) SupportsLanguage(language string) bool {
	if p.Language == "" {
		return true
	}
	if language == "" {
		language = "und"
	}
	return ContainsContainer(p.Language, language)
}

// FlexibleInt accepts the string-or-number representation emitted by legacy
// device-profile JSON while retaining integer comparison semantics in Go.
type FlexibleInt int

func (v *FlexibleInt) UnmarshalJSON(data []byte) error {
	var number int
	if err := json.Unmarshal(data, &number); err == nil {
		*v = FlexibleInt(number)
		return nil
	}
	var text string
	if err := json.Unmarshal(data, &text); err != nil {
		return err
	}
	parsed, err := strconv.Atoi(text)
	if err != nil {
		return err
	}
	*v = FlexibleInt(parsed)
	return nil
}

type TranscodingProfile struct {
	Container                 string              `json:"Container"`
	Type                      DlnaProfileType     `json:"Type"`
	VideoCodec                string              `json:"VideoCodec"`
	AudioCodec                string              `json:"AudioCodec"`
	Protocol                  MediaStreamProtocol `json:"Protocol"`
	EstimateContentLength     bool                `json:"EstimateContentLength"`
	EnableMpegtsM2TsMode      bool                `json:"EnableMpegtsM2TsMode"`
	TranscodeSeekInfo         TranscodeSeekInfo   `json:"TranscodeSeekInfo"`
	CopyTimestamps            bool                `json:"CopyTimestamps"`
	Context                   EncodingContext     `json:"Context"`
	EnableSubtitlesInManifest bool                `json:"EnableSubtitlesInManifest"`
	MaxAudioChannels          string              `json:"MaxAudioChannels"`
	MinSegments               FlexibleInt         `json:"MinSegments"`
	SegmentLength             FlexibleInt         `json:"SegmentLength"`
	BreakOnNonKeyFrames       *bool               `json:"BreakOnNonKeyFrames,omitempty"`
	Conditions                []ProfileCondition  `json:"Conditions"`
	EnableAudioVbrEncoding    bool                `json:"EnableAudioVbrEncoding"`

	maxAudioChannelsSet bool
}

type DeviceProfile struct {
	Name                             string               `json:"Name,omitempty"`
	ID                               string               `json:"Id,omitempty"`
	MaxStreamingBitrate              *int                 `json:"MaxStreamingBitrate,omitempty"`
	MaxStaticBitrate                 *int                 `json:"MaxStaticBitrate,omitempty"`
	MusicStreamingTranscodingBitrate *int                 `json:"MusicStreamingTranscodingBitrate,omitempty"`
	MaxStaticMusicBitrate            *int                 `json:"MaxStaticMusicBitrate,omitempty"`
	DirectPlayProfiles               []DirectPlayProfile  `json:"DirectPlayProfiles"`
	TranscodingProfiles              []TranscodingProfile `json:"TranscodingProfiles"`
	ContainerProfiles                []ContainerProfile   `json:"ContainerProfiles"`
	CodecProfiles                    []CodecProfile       `json:"CodecProfiles"`
	SubtitleProfiles                 []SubtitleProfile    `json:"SubtitleProfiles"`
}

type MediaAttachment struct {
	Codec       string `json:"Codec,omitempty"`
	CodecTag    string `json:"CodecTag,omitempty"`
	Comment     string `json:"Comment,omitempty"`
	Index       int    `json:"Index"`
	FileName    string `json:"FileName,omitempty"`
	MimeType    string `json:"MimeType,omitempty"`
	DeliveryURL string `json:"DeliveryUrl,omitempty"`
}

type MediaStream struct {
	Codec                     string                  `json:"Codec"`
	CodecTag                  *string                 `json:"CodecTag,omitempty"`
	Language                  *string                 `json:"Language,omitempty"`
	ColorRange                *string                 `json:"ColorRange,omitempty"`
	ColorSpace                *string                 `json:"ColorSpace,omitempty"`
	ColorTransfer             *string                 `json:"ColorTransfer,omitempty"`
	ColorPrimaries            *string                 `json:"ColorPrimaries,omitempty"`
	DvVersionMajor            *int                    `json:"DvVersionMajor,omitempty"`
	DvVersionMinor            *int                    `json:"DvVersionMinor,omitempty"`
	DvProfile                 *int                    `json:"DvProfile,omitempty"`
	DvLevel                   *int                    `json:"DvLevel,omitempty"`
	RpuPresentFlag            *int                    `json:"RpuPresentFlag,omitempty"`
	ElPresentFlag             *int                    `json:"ElPresentFlag,omitempty"`
	BlPresentFlag             *int                    `json:"BlPresentFlag,omitempty"`
	DvBlSignalCompatibilityID *int                    `json:"DvBlSignalCompatibilityId,omitempty"`
	Rotation                  *int                    `json:"Rotation,omitempty"`
	Comment                   *string                 `json:"Comment,omitempty"`
	TimeBase                  string                  `json:"TimeBase,omitempty"`
	CodecTimeBase             *string                 `json:"CodecTimeBase,omitempty"`
	Title                     *string                 `json:"Title,omitempty"`
	Hdr10PlusPresentFlag      *bool                   `json:"Hdr10PlusPresentFlag,omitempty"`
	NalLengthSize             *string                 `json:"NalLengthSize,omitempty"`
	IsInterlaced              bool                    `json:"IsInterlaced"`
	IsAVC                     *bool                   `json:"IsAVC,omitempty"`
	ChannelLayout             *string                 `json:"ChannelLayout,omitempty"`
	BitRate                   *int                    `json:"BitRate,omitempty"`
	BitDepth                  *int                    `json:"BitDepth,omitempty"`
	HighTier                  bool                    `json:"HevcHighTier,omitempty"`
	RefFrames                 *int                    `json:"RefFrames,omitempty"`
	PacketLength              *int                    `json:"PacketLength,omitempty"`
	Channels                  *int                    `json:"Channels,omitempty"`
	SampleRate                *int                    `json:"SampleRate,omitempty"`
	IsDefault                 bool                    `json:"IsDefault"`
	IsForced                  bool                    `json:"IsForced"`
	IsHearingImpaired         bool                    `json:"IsHearingImpaired"`
	IsOriginal                bool                    `json:"IsOriginal"`
	Height                    *int                    `json:"Height,omitempty"`
	Width                     *int                    `json:"Width,omitempty"`
	AverageFrameRate          *float32                `json:"AverageFrameRate,omitempty"`
	RealFrameRate             *float32                `json:"RealFrameRate,omitempty"`
	ReferenceFrameRateValue   *float32                `json:"ReferenceFrameRate,omitempty"`
	Profile                   *string                 `json:"Profile,omitempty"`
	Type                      StreamType              `json:"Type"`
	AspectRatio               *string                 `json:"AspectRatio,omitempty"`
	Index                     int                     `json:"Index"`
	Score                     *int                    `json:"Score,omitempty"`
	IsExternal                bool                    `json:"IsExternal"`
	DeliveryMethod            *SubtitleDeliveryMethod `json:"DeliveryMethod,omitempty"`
	DeliveryURL               string                  `json:"DeliveryUrl,omitempty"`
	IsExternalURL             *bool                   `json:"IsExternalUrl,omitempty"`
	SupportsExternalStream    bool                    `json:"SupportsExternalStream"`
	Path                      string                  `json:"Path,omitempty"`
	PixelFormat               *string                 `json:"PixelFormat,omitempty"`
	Level                     *float64                `json:"Level,omitempty"`
	IsAnamorphic              *bool                   `json:"IsAnamorphic,omitempty"`
	LocalizedUndefined        string                  `json:"LocalizedUndefined,omitempty"`
	LocalizedDefault          string                  `json:"LocalizedDefault,omitempty"`
	LocalizedForced           string                  `json:"LocalizedForced,omitempty"`
	LocalizedExternal         string                  `json:"LocalizedExternal,omitempty"`
	LocalizedHearingImpaired  string                  `json:"LocalizedHearingImpaired,omitempty"`
	LocalizedLanguage         string                  `json:"LocalizedLanguage,omitempty"`
	LocalizedOriginal         string                  `json:"LocalizedOriginal,omitempty"`
}

type numericWireKind uint8

const (
	numericWireInt32 numericWireKind = iota
	numericWireInt64
	numericWireFloat32
	numericWireFloat64
)

var numericWireFields = map[string]numericWireKind{
	"dvversionmajor": numericWireInt32, "dvversionminor": numericWireInt32, "dvprofile": numericWireInt32, "dvlevel": numericWireInt32, "rpupresentflag": numericWireInt32, "elpresentflag": numericWireInt32, "blpresentflag": numericWireInt32, "dvblsignalcompatibilityid": numericWireInt32, "rotation": numericWireInt32,
	"bitrate": numericWireInt32, "bitdepth": numericWireInt32, "refframes": numericWireInt32, "packetlength": numericWireInt32, "channels": numericWireInt32, "samplerate": numericWireInt32, "height": numericWireInt32, "width": numericWireInt32, "index": numericWireInt32, "score": numericWireInt32, "bufferms": numericWireInt32, "fallbackmaxstreamingbitrate": numericWireInt32, "analyzedurationms": numericWireInt32, "defaultaudiostreamindex": numericWireInt32, "defaultsubtitlestreamindex": numericWireInt32,
	"size": numericWireInt64, "runtimeticks": numericWireInt64,
	"averageframerate": numericWireFloat32, "realframerate": numericWireFloat32, "referenceframerate": numericWireFloat32,
	"level": numericWireFloat64,
}

func canonicalNumericWire(text string, kind numericWireKind) (json.Number, error) {
	switch kind {
	case numericWireInt32:
		value, err := strconv.ParseInt(text, 10, 32)
		if err != nil {
			return "", err
		}
		return json.Number(strconv.FormatInt(value, 10)), nil
	case numericWireInt64:
		value, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			return "", err
		}
		return json.Number(strconv.FormatInt(value, 10)), nil
	case numericWireFloat32:
		value, err := strconv.ParseFloat(text, 32)
		if err != nil {
			return "", err
		}
		return json.Number(strconv.FormatFloat(value, 'g', -1, 32)), nil
	default:
		value, err := strconv.ParseFloat(text, 64)
		if err != nil {
			return "", err
		}
		return json.Number(strconv.FormatFloat(value, 'g', -1, 64)), nil
	}
}

func normalizeNumericWireJSON(data []byte) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var visit func(any) error
	visit = func(current any) error {
		switch node := current.(type) {
		case map[string]any:
			for key, child := range node {
				if kind, numeric := numericWireFields[strings.ToLower(key)]; numeric {
					var text string
					switch raw := child.(type) {
					case string:
						text = raw
					case json.Number:
						text = raw.String()
					default:
						if err := visit(child); err != nil {
							return err
						}
						continue
					}
					canonical, err := canonicalNumericWire(text, kind)
					if err != nil {
						return err
					}
					node[key] = canonical
					continue
				}
				if err := visit(child); err != nil {
					return err
				}
			}
		case []any:
			for _, child := range node {
				if err := visit(child); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := visit(value); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

func (s *MediaStream) UnmarshalJSON(data []byte) error {
	normalized, err := normalizeNumericWireJSON(data)
	if err != nil {
		return err
	}
	type plain MediaStream
	var value plain
	if err := jsonnum.Unmarshal(normalized, &value); err != nil {
		return err
	}
	*s = MediaStream(value)
	return nil
}

func hasJSONField(data []byte, name string) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil {
		return false
	}
	for key := range fields {
		if strings.EqualFold(key, name) {
			return true
		}
	}
	return false
}

func stringWhenPresent(value string, present bool) *string {
	if !present && value == "" {
		return nil
	}
	return &value
}

func (p *DirectPlayProfile) UnmarshalJSON(data []byte) error {
	type plain DirectPlayProfile
	var value plain
	if err := jsonnum.Unmarshal(data, &value); err != nil {
		return err
	}
	*p = DirectPlayProfile(value)
	p.audioCodecSet = hasJSONField(data, "AudioCodec")
	p.videoCodecSet = hasJSONField(data, "VideoCodec")
	return nil
}

func (p DirectPlayProfile) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Container  string          `json:"Container"`
		AudioCodec *string         `json:"AudioCodec,omitempty"`
		VideoCodec *string         `json:"VideoCodec,omitempty"`
		Type       DlnaProfileType `json:"Type"`
	}{p.Container, stringWhenPresent(p.AudioCodec, p.audioCodecSet), stringWhenPresent(p.VideoCodec, p.videoCodecSet), p.Type})
}

func (p *CodecProfile) UnmarshalJSON(data []byte) error {
	type plain CodecProfile
	var value plain
	if err := jsonnum.Unmarshal(data, &value); err != nil {
		return err
	}
	*p = CodecProfile(value)
	p.codecSet = hasJSONField(data, "Codec")
	p.containerSet = hasJSONField(data, "Container")
	p.subContainerSet = hasJSONField(data, "SubContainer")
	return nil
}

func (p CodecProfile) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Type            CodecType          `json:"Type"`
		Conditions      []ProfileCondition `json:"Conditions"`
		ApplyConditions []ProfileCondition `json:"ApplyConditions"`
		Codec           *string            `json:"Codec,omitempty"`
		Container       *string            `json:"Container,omitempty"`
		SubContainer    *string            `json:"SubContainer,omitempty"`
	}{p.Type, p.Conditions, p.ApplyConditions, stringWhenPresent(p.Codec, p.codecSet), stringWhenPresent(p.Container, p.containerSet), stringWhenPresent(p.SubContainer, p.subContainerSet)})
}

func (p *ContainerProfile) UnmarshalJSON(data []byte) error {
	type plain ContainerProfile
	var value plain
	if err := jsonnum.Unmarshal(data, &value); err != nil {
		return err
	}
	*p = ContainerProfile(value)
	p.containerSet = hasJSONField(data, "Container")
	p.subContainerSet = hasJSONField(data, "SubContainer")
	return nil
}

func (p ContainerProfile) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Type         DlnaProfileType    `json:"Type"`
		Conditions   []ProfileCondition `json:"Conditions"`
		Container    *string            `json:"Container,omitempty"`
		SubContainer *string            `json:"SubContainer,omitempty"`
	}{p.Type, p.Conditions, stringWhenPresent(p.Container, p.containerSet), stringWhenPresent(p.SubContainer, p.subContainerSet)})
}

func (p *TranscodingProfile) UnmarshalJSON(data []byte) error {
	type plain TranscodingProfile
	var value plain
	value.EnableAudioVbrEncoding = true
	if err := jsonnum.Unmarshal(data, &value); err != nil {
		return err
	}
	*p = TranscodingProfile(value)
	p.maxAudioChannelsSet = hasJSONField(data, "MaxAudioChannels")
	return nil
}

func (p TranscodingProfile) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Container                 string              `json:"Container"`
		Type                      DlnaProfileType     `json:"Type"`
		VideoCodec                string              `json:"VideoCodec"`
		AudioCodec                string              `json:"AudioCodec"`
		Protocol                  MediaStreamProtocol `json:"Protocol"`
		EstimateContentLength     bool                `json:"EstimateContentLength"`
		EnableMpegtsM2TsMode      bool                `json:"EnableMpegtsM2TsMode"`
		TranscodeSeekInfo         TranscodeSeekInfo   `json:"TranscodeSeekInfo"`
		CopyTimestamps            bool                `json:"CopyTimestamps"`
		Context                   EncodingContext     `json:"Context"`
		EnableSubtitlesInManifest bool                `json:"EnableSubtitlesInManifest"`
		MaxAudioChannels          *string             `json:"MaxAudioChannels,omitempty"`
		MinSegments               FlexibleInt         `json:"MinSegments"`
		SegmentLength             FlexibleInt         `json:"SegmentLength"`
		BreakOnNonKeyFrames       *bool               `json:"BreakOnNonKeyFrames,omitempty"`
		Conditions                []ProfileCondition  `json:"Conditions"`
		EnableAudioVbrEncoding    bool                `json:"EnableAudioVbrEncoding"`
	}{p.Container, p.Type, p.VideoCodec, p.AudioCodec, p.Protocol, p.EstimateContentLength, p.EnableMpegtsM2TsMode, p.TranscodeSeekInfo, p.CopyTimestamps, p.Context, p.EnableSubtitlesInManifest, stringWhenPresent(p.MaxAudioChannels, p.maxAudioChannelsSet), p.MinSegments, p.SegmentLength, p.BreakOnNonKeyFrames, p.Conditions, p.EnableAudioVbrEncoding})
}

func (s MediaStream) MarshalJSON() ([]byte, error) {
	type wire MediaStream
	public := s
	public.HighTier = false
	var displayTitle *string
	if s.Type == StreamTypeAudio || s.Type == StreamTypeVideo || s.Type == StreamTypeSubtitle {
		value := s.displayTitle()
		displayTitle = &value
	}
	return json.Marshal(struct {
		wire
		ReferenceFrameRate   *float32       `json:"ReferenceFrameRate,omitempty"`
		VideoRange           string         `json:"VideoRange"`
		VideoRangeType       VideoRangeType `json:"VideoRangeType"`
		VideoDoViTitle       string         `json:"VideoDoViTitle,omitempty"`
		AudioSpatialFormat   string         `json:"AudioSpatialFormat"`
		DisplayTitle         *string        `json:"DisplayTitle,omitempty"`
		IsTextSubtitleStream bool           `json:"IsTextSubtitleStream"`
	}{wire(public), s.ReferenceFrameRate(), mediaVideoRangeName(s.VideoRange()), s.VideoRangeType(), s.videoDoViTitle(), s.audioSpatialFormatName(), displayTitle, s.IsTextSubtitleStream()})
}

func mediaVideoRangeName(value VideoRange) string {
	switch value {
	case MediaVideoRangeSDR:
		return "SDR"
	case MediaVideoRangeHDR:
		return "HDR"
	default:
		return "Unknown"
	}
}

func (s MediaStream) videoDoViTitle() string {
	if s.RpuPresentFlag == nil || *s.RpuPresentFlag != 1 || s.BlPresentFlag == nil || *s.BlPresentFlag != 1 || s.DvProfile == nil {
		return ""
	}
	profile := *s.DvProfile
	if profile != 4 && profile != 5 && profile != 7 && profile != 8 && profile != 9 && profile != 10 {
		return ""
	}
	title := "Dolby Vision Profile " + strconv.Itoa(profile)
	compatibility := 0
	if s.DvBlSignalCompatibilityID != nil {
		compatibility = *s.DvBlSignalCompatibilityID
	}
	if compatibility > 0 {
		title += "." + strconv.Itoa(compatibility)
	}
	switch compatibility {
	case 1, 6:
		return title + " (HDR10)"
	case 2:
		return title + " (SDR)"
	case 4:
		return title + " (HLG)"
	default:
		return title
	}
}

func (s MediaStream) audioSpatialFormatName() string {
	profile := stringValue(s.Profile)
	if s.Type != StreamTypeAudio || profile == "" {
		return "None"
	}
	if strings.Contains(strings.ToLower(profile), "dolby atmos") {
		return "DolbyAtmos"
	}
	if strings.Contains(strings.ToLower(profile), "dts:x") {
		return "DTSX"
	}
	return "None"
}

func streamFirstToUpper(value string) string {
	if value == "" {
		return ""
	}
	first, size := utf8.DecodeRuneInString(value)
	return strings.ToUpper(string(first)) + value[size:]
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func streamResolution(width, height *int, interlaced bool) string {
	if width == nil || height == nil {
		return ""
	}
	mode := "p"
	if interlaced {
		mode = "i"
	}
	limits := []struct {
		width, height int
		name          string
	}{
		{256, 144, "144"}, {426, 240, "240"}, {640, 360, "360"}, {682, 384, "384"}, {720, 404, "404"}, {854, 480, "480"}, {960, 544, "540"}, {1024, 576, "576"}, {1280, 962, "720"}, {2560, 1440, "1080"},
	}
	for _, limit := range limits {
		if *width <= limit.width && *height <= limit.height {
			return limit.name + mode
		}
	}
	if *width <= 4096 && *height <= 3072 {
		return "4K"
	}
	if *width <= 8192 && *height <= 6144 {
		return "8K"
	}
	return ""
}

func streamAudioCodecName(codec string) string {
	if strings.EqualFold(codec, "ac3") {
		return "Dolby Digital"
	}
	if strings.EqualFold(codec, "eac3") {
		return "Dolby Digital+"
	}
	if strings.EqualFold(codec, "dca") {
		return "DTS"
	}
	return strings.ToUpper(codec)
}

func streamTitleWithAttributes(title string, attributes []string, separator string) string {
	if title == "" {
		return strings.Join(attributes, separator)
	}
	for _, attribute := range attributes {
		if !strings.Contains(strings.ToLower(title), strings.ToLower(attribute)) {
			title += " - " + attribute
		}
	}
	return title
}

func (s MediaStream) displayTitle() string {
	language := stringValue(s.Language)
	profile := stringValue(s.Profile)
	channelLayout := stringValue(s.ChannelLayout)
	title := stringValue(s.Title)
	switch s.Type {
	case StreamTypeAudio:
		attributes := make([]string, 0, 6)
		if language != "" && !strings.EqualFold(language, "mis") && !strings.EqualFold(language, "mul") && !strings.EqualFold(language, "und") && !strings.EqualFold(language, "zxx") {
			localized := s.LocalizedLanguage
			if localized == "" {
				localized = language
			}
			attributes = append(attributes, streamFirstToUpper(localized))
		}
		if profile != "" && !strings.EqualFold(profile, "lc") {
			attributes = append(attributes, profile)
		} else if s.Codec != "" {
			attributes = append(attributes, streamAudioCodecName(s.Codec))
		}
		if channelLayout != "" {
			attributes = append(attributes, streamFirstToUpper(channelLayout))
		} else if s.Channels != nil {
			attributes = append(attributes, strconv.Itoa(*s.Channels)+" ch")
		}
		if s.IsDefault {
			value := s.LocalizedDefault
			if value == "" {
				value = "Default"
			}
			attributes = append(attributes, value)
		}
		if s.IsExternal {
			value := s.LocalizedExternal
			if value == "" {
				value = "External"
			}
			attributes = append(attributes, value)
		}
		if s.IsOriginal {
			value := s.LocalizedOriginal
			if value == "" {
				value = "Original"
			}
			attributes = append(attributes, value)
		}
		return streamTitleWithAttributes(title, attributes, " - ")
	case StreamTypeVideo:
		attributes := make([]string, 0, 3)
		if resolution := streamResolution(s.Width, s.Height, s.IsInterlaced); resolution != "" {
			attributes = append(attributes, resolution)
		}
		if s.Codec != "" {
			attributes = append(attributes, strings.ToUpper(s.Codec))
		}
		if dovi := s.videoDoViTitle(); dovi != "" {
			attributes = append(attributes, dovi)
		} else if rangeName := mediaVideoRangeName(s.VideoRange()); rangeName != "Unknown" {
			attributes = append(attributes, rangeName)
		}
		return streamTitleWithAttributes(title, attributes, " ")
	case StreamTypeSubtitle:
		attributes := make([]string, 0, 5)
		if language != "" {
			localized := s.LocalizedLanguage
			if localized == "" {
				localized = language
			}
			attributes = append(attributes, streamFirstToUpper(localized))
		} else if s.LocalizedUndefined != "" {
			attributes = append(attributes, s.LocalizedUndefined)
		} else {
			attributes = append(attributes, "Und")
		}
		if s.IsHearingImpaired {
			value := s.LocalizedHearingImpaired
			if value == "" {
				value = "Hearing Impaired"
			}
			attributes = append(attributes, value)
		}
		if s.IsDefault {
			value := s.LocalizedDefault
			if value == "" {
				value = "Default"
			}
			attributes = append(attributes, value)
		}
		if s.IsForced {
			value := s.LocalizedForced
			if value == "" {
				value = "Forced"
			}
			attributes = append(attributes, value)
		}
		if s.Codec != "" {
			attributes = append(attributes, strings.ToUpper(s.Codec))
		}
		if s.IsExternal {
			value := s.LocalizedExternal
			if value == "" {
				value = "External"
			}
			attributes = append(attributes, value)
		}
		return streamTitleWithAttributes(title, attributes, " - ")
	default:
		return ""
	}
}

func enumMarshal(value int, names map[string]int) ([]byte, error) {
	for name, candidate := range names {
		if candidate == value {
			switch name {
			case "directplay":
				name = "DirectPlay"
			case "directstream":
				name = "DirectStream"
			case "videoaudio":
				name = "VideoAudio"
			case "videofile":
				name = "VideoFile"
			case "bluray":
				name = "BluRay"
			case "hdvd":
				name = "HDVD"
			case "hls":
				name = "Hls"
			case "embeddedimage":
				name = "EmbeddedImage"
			case "halfsidebyside":
				name = "HalfSideBySide"
			case "fullsidebyside":
				name = "FullSideBySide"
			case "fulltopandbottom":
				name = "FullTopAndBottom"
			case "halftopandbottom":
				name = "HalfTopAndBottom"
			case "dtsx":
				name = "DTSX"
			case "dolbyatmos":
				name = "DolbyAtmos"
			default:
				name = strings.ToUpper(name[:1]) + name[1:]
			}
			return json.Marshal(name)
		}
	}
	return json.Marshal(value)
}

func (v DlnaProfileType) MarshalJSON() ([]byte, error) {
	return enumMarshal(int(v), enumNames[DlnaProfileType(0)])
}
func (v CodecType) MarshalJSON() ([]byte, error) { return enumMarshal(int(v), enumNames[CodecType(0)]) }
func (v EncodingContext) MarshalJSON() ([]byte, error) {
	return enumMarshal(int(v), enumNames[EncodingContext(0)])
}

func (v PlayMethod) MarshalJSON() ([]byte, error) {
	return enumMarshal(int(v), enumNames[PlayMethod(0)])
}

func (v MediaProtocol) MarshalJSON() ([]byte, error) {
	return enumMarshal(int(v), enumNames[MediaProtocol(0)])
}

func (v MediaStreamProtocol) MarshalJSON() ([]byte, error) {
	for name, candidate := range enumNames[MediaStreamProtocol(0)] {
		if candidate == int(v) {
			return json.Marshal(name)
		}
	}
	return json.Marshal(int(v))
}

func (v StreamType) MarshalJSON() ([]byte, error) {
	return enumMarshal(int(v), enumNames[StreamType(0)])
}

func (v SubtitleDeliveryMethod) MarshalJSON() ([]byte, error) {
	return enumMarshal(int(v), enumNames[SubtitleDeliveryMethod(0)])
}
func (v VideoType) MarshalJSON() ([]byte, error) { return enumMarshal(int(v), enumNames[VideoType(0)]) }
func (v TranscodeSeekInfo) MarshalJSON() ([]byte, error) {
	return enumMarshal(int(v), enumNames[TranscodeSeekInfo(0)])
}

func (v MediaSourceType) MarshalJSON() ([]byte, error) {
	return enumMarshal(int(v), enumNames[MediaSourceType(0)])
}
func (v IsoType) MarshalJSON() ([]byte, error) { return enumMarshal(int(v), enumNames[IsoType(0)]) }
func (v Video3DFormat) MarshalJSON() ([]byte, error) {
	return enumMarshal(int(v), enumNames[Video3DFormat(0)])
}

func (v AudioSpatialFormat) MarshalJSON() ([]byte, error) {
	return enumMarshal(int(v), enumNames[AudioSpatialFormat(0)])
}

type MediaSourceInfo struct {
	Protocol                            MediaProtocol             `json:"Protocol"`
	ID                                  string                    `json:"Id"`
	idSet                               bool                      `json:"-"`
	idNull                              bool                      `json:"-"`
	FileFacts                           SourceFileFacts           `json:"-"`
	SourceRevision                      string                    `json:"-"`
	Path                                string                    `json:"Path,omitempty"`
	EncoderPath                         string                    `json:"EncoderPath,omitempty"`
	EncoderProtocol                     *MediaProtocol            `json:"EncoderProtocol,omitempty"`
	Type                                MediaSourceType           `json:"Type"`
	Container                           string                    `json:"Container,omitempty"`
	Size                                *int64                    `json:"Size,omitempty"`
	Name                                string                    `json:"Name,omitempty"`
	IsRemote                            bool                      `json:"IsRemote"`
	ETag                                string                    `json:"ETag,omitempty"`
	RunTimeTicks                        *int64                    `json:"RunTimeTicks,omitempty"`
	ReadAtNativeFramerate               bool                      `json:"ReadAtNativeFramerate"`
	IgnoreDts                           bool                      `json:"IgnoreDts"`
	IgnoreIndex                         bool                      `json:"IgnoreIndex"`
	GenPtsInput                         bool                      `json:"GenPtsInput"`
	SupportsTranscoding                 bool                      `json:"SupportsTranscoding"`
	SupportsDirectStream                bool                      `json:"SupportsDirectStream"`
	SupportsDirectPlay                  bool                      `json:"SupportsDirectPlay"`
	IsInfiniteStream                    bool                      `json:"IsInfiniteStream"`
	UseMostCompatibleTranscodingProfile bool                      `json:"UseMostCompatibleTranscodingProfile"`
	RequiresOpening                     bool                      `json:"RequiresOpening"`
	OpenToken                           string                    `json:"OpenToken,omitempty"`
	RequiresClosing                     bool                      `json:"RequiresClosing"`
	LiveStreamID                        string                    `json:"LiveStreamId,omitempty"`
	BufferMs                            *int                      `json:"BufferMs,omitempty"`
	RequiresLooping                     bool                      `json:"RequiresLooping"`
	SupportsProbing                     bool                      `json:"SupportsProbing"`
	VideoType                           *VideoType                `json:"VideoType,omitempty"`
	IsoType                             *IsoType                  `json:"IsoType,omitempty"`
	Video3DFormat                       *Video3DFormat            `json:"Video3DFormat,omitempty"`
	MediaStreams                        []MediaStream             `json:"MediaStreams"`
	MediaAttachments                    []MediaAttachment         `json:"MediaAttachments"`
	Formats                             []string                  `json:"Formats"`
	Bitrate                             *int                      `json:"Bitrate,omitempty"`
	FallbackMaxStreamingBitrate         *int                      `json:"FallbackMaxStreamingBitrate,omitempty"`
	Timestamp                           *TransportStreamTimestamp `json:"Timestamp,omitempty"`
	RequiredHTTPHeaders                 map[string]string         `json:"RequiredHttpHeaders"`
	TranscodingURL                      string                    `json:"TranscodingUrl,omitempty"`
	TranscodingSubProtocol              MediaStreamProtocol       `json:"TranscodingSubProtocol"`
	TranscodingContainer                string                    `json:"TranscodingContainer,omitempty"`
	AnalyzeDurationMs                   *int                      `json:"AnalyzeDurationMs,omitempty"`
	TranscodeReasons                    TranscodeReason           `json:"-"`
	DefaultAudioIndexSource             AudioIndexSource          `json:"-"`
	DefaultAudioStreamIndex             *int                      `json:"DefaultAudioStreamIndex,omitempty"`
	DefaultSubtitleStreamIndex          *int                      `json:"DefaultSubtitleStreamIndex,omitempty"`
	HasSegments                         bool                      `json:"HasSegments"`
}

func (m *MediaSourceInfo) VideoStream() *MediaStream {
	for i := range m.MediaStreams {
		if m.MediaStreams[i].Type == StreamTypeVideo {
			return &m.MediaStreams[i]
		}
	}
	return nil
}

func (m *MediaSourceInfo) DefaultAudioStream(index *int) *MediaStream {
	if index != nil && *index != -1 {
		if s := m.MediaStream(StreamTypeAudio, *index); s != nil {
			return s
		}
	}
	for i := range m.MediaStreams {
		if m.MediaStreams[i].Type == StreamTypeAudio && m.MediaStreams[i].IsDefault {
			return &m.MediaStreams[i]
		}
	}
	for i := range m.MediaStreams {
		if m.MediaStreams[i].Type == StreamTypeAudio {
			return &m.MediaStreams[i]
		}
	}
	return nil
}

func (m *MediaSourceInfo) MediaStream(kind StreamType, index int) *MediaStream {
	for i := range m.MediaStreams {
		if m.MediaStreams[i].Type == kind && m.MediaStreams[i].Index == index {
			return &m.MediaStreams[i]
		}
	}
	return nil
}

func (m *MediaSourceInfo) StreamCount(kind StreamType) *int {
	if len(m.MediaStreams) == 0 {
		return nil
	}
	n := 0
	for i := range m.MediaStreams {
		if m.MediaStreams[i].Type == kind {
			n++
		}
	}
	return &n
}

func (m *MediaSourceInfo) IsSecondaryAudio(stream *MediaStream) *bool {
	if stream == nil {
		return nil
	}
	if stream.IsExternal {
		v := false
		return &v
	}
	for i := range m.MediaStreams {
		if m.MediaStreams[i].Type == StreamTypeAudio && !m.MediaStreams[i].IsExternal {
			v := m.MediaStreams[i].Index != stream.Index

			return &v
		}
	}
	return nil
}

func NewMediaSourceInfo() MediaSourceInfo {
	return MediaSourceInfo{
		SupportsTranscoding:  true,
		SupportsDirectStream: true,
		SupportsDirectPlay:   true,
		SupportsProbing:      true,
		MediaStreams:         make([]MediaStream, 0),
		MediaAttachments:     make([]MediaAttachment, 0),
		Formats:              make([]string, 0),
		RequiredHTTPHeaders:  make(map[string]string),
	}
}

func (m *MediaSourceInfo) UnmarshalJSON(data []byte) error {
	normalized, err := normalizeNumericWireJSON(data)
	if err != nil {
		return err
	}
	value := NewMediaSourceInfo()
	type plain MediaSourceInfo
	if err := jsonnum.Unmarshal(normalized, (*plain)(&value)); err != nil {
		return err
	}
	value.idSet = hasJSONField(data, "Id")
	var rawFields map[string]json.RawMessage
	_ = json.Unmarshal(data, &rawFields)
	for key, raw := range rawFields {
		if strings.EqualFold(key, "Id") {
			value.idNull = strings.TrimSpace(string(raw)) == "null"
			break
		}
	}
	*m = value
	return nil
}

func (m MediaSourceInfo) MarshalJSON() ([]byte, error) {
	type wire MediaSourceInfo
	var streams *[]MediaStream
	var attachments *[]MediaAttachment
	var formats *[]string
	var headers *map[string]string
	var id *string
	if !m.idNull && (m.ID != "" || m.idSet) {
		id = &m.ID
	}
	if m.MediaStreams != nil {
		streams = &m.MediaStreams
	}
	if m.MediaAttachments != nil {
		attachments = &m.MediaAttachments
	}
	if m.Formats != nil {
		formats = &m.Formats
	}
	if m.RequiredHTTPHeaders != nil {
		headers = &m.RequiredHTTPHeaders
	}
	return json.Marshal(struct {
		wire
		ID                  *string            `json:"Id,omitempty"`
		MediaStreams        *[]MediaStream     `json:"MediaStreams,omitempty"`
		MediaAttachments    *[]MediaAttachment `json:"MediaAttachments,omitempty"`
		Formats             *[]string          `json:"Formats,omitempty"`
		RequiredHTTPHeaders *map[string]string `json:"RequiredHttpHeaders,omitempty"`
	}{wire(m), id, streams, attachments, formats, headers})
}

type MediaOptions struct {
	EnableDirectPlay   bool
	EnableDirectStream bool
	ForceDirectPlay    bool

	ForceDirectStream                   bool
	AllowAudioStreamCopy                bool
	AllowVideoStreamCopy                bool
	AlwaysBurnInSubtitleWhenTranscoding bool
	ItemID                              string
	MediaSources                        []MediaSourceInfo
	Profile                             *DeviceProfile
	MediaSourceID                       string
	DeviceID                            string
	MaxAudioChannels                    *int
	MaxBitrate                          *int
	Context                             EncodingContext
	AudioTranscodingBitrate             *int
	AudioStreamIndex                    *int
	SubtitleStreamIndex                 *int
}

func NewMediaOptions() MediaOptions {
	return MediaOptions{EnableDirectPlay: true, EnableDirectStream: true, Context: ContextStreaming}
}

func (o MediaOptions) MaxBitrateFor(isAudio bool) *int {
	if o.MaxBitrate != nil {
		return o.MaxBitrate
	}
	if o.Profile == nil {
		return nil
	}
	if o.Context == ContextStatic {
		if isAudio && o.Profile.MaxStaticMusicBitrate != nil {
			return o.Profile.MaxStaticMusicBitrate
		}
		return o.Profile.MaxStaticBitrate
	}
	return o.Profile.MaxStreamingBitrate
}

type TranscoderSupport interface {
	CanEncodeToAudioCodec(codec string) bool
	CanEncodeToSubtitleCodec(codec string) bool
	CanExtractSubtitles(codec string) bool
}

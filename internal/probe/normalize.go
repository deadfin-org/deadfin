// SPDX-License-Identifier: GPL-2.0-only

package probe

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/deadfin-org/deadfin/internal/playback"
)

// Options supplies the non-ffprobe facts required by upstream normalization.
type Options struct {
	IsAudio   bool
	Path      string
	Protocol  string
	VideoType string
}

// Normalize decodes ffprobe JSON and ports ProbeResultNormalizer, including
// the music and container tag-mining surface completed in P8. Context is
// required because transport-stream timestamp normalization may read Path.
func Normalize(ctx context.Context, raw []byte, options Options) (MediaInfo, error) {
	if err := ctx.Err(); err != nil {
		return MediaInfo{}, err
	}
	var result Result
	if err := json.Unmarshal(raw, &result); err != nil {
		return MediaInfo{}, fmt.Errorf("decoding ffprobe JSON: %w", err)
	}
	if result.Streams == nil && result.Format == nil {
		return MediaInfo{}, fmt.Errorf("ffprobe result has neither streams nor format")
	}

	protocol, err := parseProtocol(options.Protocol)
	if err != nil {
		return MediaInfo{}, err
	}
	videoType, err := parseVideoType(options.VideoType)
	if err != nil {
		return MediaInfo{}, err
	}
	source := playback.NewMediaSourceInfo()
	source.Path = options.Path
	source.Protocol = protocol
	source.VideoType = videoType
	info := MediaInfo{
		MediaSourceInfo: source,
		Chapters:        []Chapter{},
		Artists:         []string{},
		AlbumArtists:    []string{},
		Studios:         []string{},
		Genres:          []string{},
		People:          []MediaPerson{},
		ProviderIDs:     map[string]string{},
	}
	for _, input := range result.Streams {
		if attachment, ok := normalizeAttachment(input); ok {
			info.MediaAttachments = append(info.MediaAttachments, attachment)
		}
		normalized, ok, err := normalizeStream(input, result.Format, result.Frames, options.IsAudio)
		if err != nil {
			return MediaInfo{}, err
		}
		if ok && (normalized.Type != playback.StreamTypeSubtitle || strings.TrimSpace(normalized.Codec) != "") {
			info.MediaStreams = append(info.MediaStreams, normalized)
		}
	}
	if result.Format != nil {
		info.Container = NormalizeFormat(result.Format.FormatName, info.MediaStreams)
		info.Bitrate = parseInteger(result.Format.BitRate)
		if info.Size, err = parseFormatSize(result.Format.Size); err != nil {
			return MediaInfo{}, err
		}
	}
	var runtime string
	if options.IsAudio {
		for _, stream := range result.Streams {
			if strings.EqualFold(stream.CodecType, "audio") {
				runtime = stream.Duration
				break
			}
		}
	}
	if runtime == "" && result.Format != nil {
		runtime = result.Format.Duration
	}
	if runtime != "" {
		if info.RunTimeTicks, err = parseFormatDuration(runtime); err != nil {
			return MediaInfo{}, err
		}
		info.RunTimeTicks = correctMP3GaplessTicks(info.RunTimeTicks, result)
	}
	normalizeMetadata(&info, result, options.IsAudio)
	if err := normalizeChapters(&info, result.Chapters); err != nil {
		return MediaInfo{}, err
	}
	if err := applyTimestamp(ctx, &info, options); err != nil {
		return MediaInfo{}, err
	}
	estimateMissingBitrates(&info)
	return info, nil
}

func normalizeStream(input stream, source *format, frames []frame, isAudio bool) (playback.MediaStream, bool, error) {
	stream := playback.MediaStream{
		Codec:          input.CodecName,
		Profile:        optionalString(input.Profile),
		HighTier:       hevcHighTier(input.CodecName, input.ExtraData),
		Width:          intPointer(input.Width),
		Height:         intPointer(input.Height),
		Level:          levelPointer(input.Level),
		Index:          int(input.Index),
		PixelFormat:    optionalString(input.PixelFormat),
		NalLengthSize:  optionalString(string(input.NALLengthSize)),
		IsAVC:          boolPointer(input.IsAVC),
		TimeBase:       input.TimeBase,
		CodecTimeBase:  optionalString(input.CodecTimeBase),
		ColorRange:     optionalString(input.ColorRange),
		ColorSpace:     optionalString(input.ColorSpace),
		ColorTransfer:  optionalString(input.ColorTransfer),
		ColorPrimaries: optionalString(input.ColorPrimaries),
	}
	stream.Language = optionalString(tag(input.Tags, "language"))
	stream.Comment = optionalString(tag(input.Tags, "comment"))
	stream.Title = optionalString(tag(input.Tags, "title"))
	if codecTag := nonBlank(input.CodecTagString); codecTag != "" && !strings.Contains(codecTag, "[0]") {
		stream.CodecTag = &codecTag
	}

	switch strings.ToLower(input.CodecType) {
	case "audio":
		stream.Type = playback.StreamTypeAudio
		stream.Channels = intPointer(input.Channels)
		stream.SampleRate = parseInteger(input.SampleRate)
		stream.ChannelLayout = optionalString(parseChannelLayout(input.ChannelLayout))
		stream.BitDepth = bitDepth(input)
		stream.Title = fallbackTitle(stream.Title, input.Tags, "SoundHandler")
		stream.LocalizedDefault = "Default"
		stream.LocalizedExternal = "External"
		stream.LocalizedOriginal = "Original"
		stream.LocalizedLanguage = derefString(stream.Language)
	case "subtitle":
		stream.Type = playback.StreamTypeSubtitle
		stream.Codec = normalizeSubtitleCodec(stream.Codec)
		stream.Title = fallbackTitle(stream.Title, input.Tags, "SubtitleHandler")
		stream.LocalizedUndefined = "Undefined"
		stream.LocalizedDefault = "Default"
		stream.LocalizedForced = "Forced"
		stream.LocalizedExternal = "External"
		stream.LocalizedHearingImpaired = "HearingImpaired"
		stream.LocalizedLanguage = derefString(stream.Language)
	case "video":
		stream.AverageFrameRate = FrameRate(input.AverageFrameRate)
		stream.RealFrameRate = FrameRate(input.RealFrameRate)
		stream.IsInterlaced = input.FieldOrder != "" && !strings.EqualFold(input.FieldOrder, "progressive")
		if needsAspectDimensions(input.DisplayAspectRatio) && (input.Width == nil || input.Height == nil) {
			return playback.MediaStream{}, false, fmt.Errorf("ffprobe video stream lacks dimensions for aspect ratio")
		}
		stream.Type = videoStreamType(stream.Codec, input.CodecTagString, isAudio)
		stream.AspectRatio = optionalString(AspectRatio(input.DisplayAspectRatio, intPointer(input.Width), intPointer(input.Height)))
		stream.BitDepth = bitDepth(input)
		stream.IsAnamorphic = anamorphic(input)
		if input.Refs > 0 {
			stream.RefFrames = pointer(int(input.Refs))
		}
		for _, data := range input.SideData {
			applySideData(&stream, data)
		}
		for _, frame := range frames {
			if frame.StreamIndex == nil || int(*frame.StreamIndex) != stream.Index {
				continue
			}
			for _, data := range frame.SideData {
				if strings.EqualFold(data.Type, "HDR Dynamic Metadata SMPTE2094-40 (HDR10+)") {
					stream.Hdr10PlusPresentFlag = pointer(true)
				}
			}
		}
	case "data":
		stream.Type = playback.StreamTypeData
	case "attachment":
		return playback.MediaStream{}, false, nil
	default:
		return playback.MediaStream{}, false, fmt.Errorf("invalid ffprobe codec type %q", input.CodecType)
	}

	stream.BitRate = parsePositiveInt(input.BitRate)
	if stream.BitRate == nil && source != nil && isAudio && stream.Type == playback.StreamTypeAudio {
		stream.BitRate = parsePositiveInt(source.BitRate)
	}
	if stream.BitRate == nil && (stream.Type == playback.StreamTypeAudio || stream.Type == playback.StreamTypeVideo) {
		var err error
		stream.BitRate, err = bitrateFromTags(input)
		if err != nil {
			return playback.MediaStream{}, false, err
		}
	}
	stream.IsDefault = input.Disposition["default"] == 1
	stream.IsForced = input.Disposition["forced"] == 1
	stream.IsHearingImpaired = input.Disposition["hearing_impaired"] == 1
	stream.IsOriginal = input.Disposition["original"] == 1
	if strings.EqualFold(derefString(stream.Title), "cc") || stream.Type == playback.StreamTypeEmbeddedImage {
		stream.Title = nil
	}
	return stream, true, nil
}

func normalizeAttachment(input stream) (playback.MediaAttachment, bool) {
	if !strings.EqualFold(input.CodecType, "attachment") && input.Disposition["attached_pic"] != 1 {
		return playback.MediaAttachment{}, false
	}
	return playback.MediaAttachment{
		Codec: input.CodecName, CodecTag: nonBlank(input.CodecTagString), Index: int(input.Index),
		FileName: tag(input.Tags, "filename"), MimeType: tag(input.Tags, "mimetype"), Comment: tag(input.Tags, "comment"),
	}, true
}

func videoStreamType(codec, codecTag string, isAudio bool) playback.StreamType {
	if isAudio || strings.EqualFold(codec, "bmp") || strings.EqualFold(codec, "gif") || strings.EqualFold(codec, "png") || strings.EqualFold(codec, "webp") {
		return playback.StreamTypeEmbeddedImage
	}
	if strings.EqualFold(codec, "mjpeg") && codecTag == "" {
		return playback.StreamTypeEmbeddedImage
	}
	return playback.StreamTypeVideo
}

func applySideData(stream *playback.MediaStream, data sideData) {
	switch {
	case strings.EqualFold(data.Type, "DOVI configuration record"):
		stream.DvVersionMajor = intPointer(data.DVVersionMajor)
		stream.DvVersionMinor = intPointer(data.DVVersionMinor)
		stream.DvProfile = intPointer(data.DVProfile)
		stream.DvLevel = intPointer(data.DVLevel)
		stream.RpuPresentFlag = intPointer(data.RPUPresentFlag)
		stream.ElPresentFlag = intPointer(data.ELPresentFlag)
		stream.BlPresentFlag = intPointer(data.BLPresentFlag)
		stream.DvBlSignalCompatibilityID = intPointer(data.DVBLSignalCompatibilityID)
	case strings.EqualFold(data.Type, "Display Matrix"):
		stream.Rotation = intPointer(data.Rotation)
	case strings.EqualFold(data.Type, "Frame Cropping"):
		stream.IsAnamorphic = pointer(false)
	}
}

// NormalizeFormat ports ProbeResultNormalizer.NormalizeFormat, including the
// WebM validation needed when ffprobe reports "matroska,webm".
func NormalizeFormat(formatName string, streams []playback.MediaStream) string {
	if strings.TrimSpace(formatName) == "" {
		return ""
	}
	formats := strings.Split(formatName, ",")
	out := formats[:0]
	for _, format := range formats {
		switch {
		case strings.EqualFold(format, "mpegvideo"):
			format = "mpeg"
		case strings.EqualFold(format, "mpegts"):
			format = "ts"
		case strings.EqualFold(format, "matroska"):
			format = "mkv"
		case strings.EqualFold(format, "webm") && !webmCompatible(streams):
			continue
		}
		out = append(out, format)
	}
	return strings.Join(out, ",")
}

func webmCompatible(streams []playback.MediaStream) bool {
	for _, stream := range streams {
		switch stream.Type {
		case playback.StreamTypeVideo:
			if !equalAny(stream.Codec, "av1", "vp8", "vp9") {
				return false
			}
		case playback.StreamTypeAudio:
			if !equalAny(stream.Codec, "opus", "vorbis") {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// AspectRatio ports GetAspectRatio, including Jellyfin's compatibility buckets.
func AspectRatio(original string, width, height *int) string {
	value := original
	if value == "0:1" {
		value = ""
	}
	numerator, denominator, ok := parseRatio(value)
	if !ok || numerator <= 0 || denominator <= 0 {
		if width == nil || height == nil {
			return original
		}
		numerator, denominator = *width, *height
	}
	if numerator <= 0 || denominator <= 0 {
		return original
	}
	ratio := float64(numerator) / float64(denominator)
	switch {
	case closeTo(ratio, 1.777777778, .03):
		return "16:9"
	case closeTo(ratio, 1.3333333333, .05):
		return "4:3"
	case closeTo(ratio, 1.41, .005):
		return "1.41:1"
	case closeTo(ratio, 1.5, .005):
		return "1.5:1"
	case closeTo(ratio, 1.6, .005):
		return "1.6:1"
	case closeTo(ratio, 1.66666666667, .005):
		return "5:3"
	case closeTo(ratio, 1.85, .02):
		return "1.85:1"
	case closeTo(ratio, 2.35, .025):
		return "2.35:1"
	case closeTo(ratio, 2.4, .025):
		return "2.40:1"
	default:
		return original
	}
}

// FrameRate ports GetFrameRate. ffprobe values must be fractions. Bare numbers
// are deliberately rejected like upstream.
func FrameRate(value string) *float32 {
	parts := strings.Split(value, "/")
	if len(parts) != 2 {
		return nil
	}
	for index := range parts {
		parts[index] = strings.TrimSpace(parts[index])
	}
	if !isSignedInteger(parts[0]) || !isSignedInteger(parts[1]) {
		return nil
	}
	numerator, err1 := strconv.ParseFloat(parts[0], 32)
	denominator, err2 := strconv.ParseFloat(parts[1], 32)
	if err1 != nil || err2 != nil || denominator == 0 {
		return nil
	}
	result := float32(numerator / denominator)
	return &result
}

func isSignedInteger(value string) bool {
	if value == "" {
		return false
	}
	for index, r := range value {
		if (r == '+' || r == '-') && index == 0 {
			continue
		}
		if r < '0' || r > '9' {
			return false
		}
	}
	return len(value) > 1 || value[0] != '+' && value[0] != '-'
}

func anamorphic(input stream) *bool {
	if input.SampleAspectRatio == "" && input.DisplayAspectRatio == "" {
		return pointer(false)
	}
	if nearSquarePixelSAR(input.SampleAspectRatio) {
		return pointer(false)
	}
	if input.SampleAspectRatio != "0:1" {
		return pointer(true)
	}
	if input.DisplayAspectRatio == "0:1" {
		return pointer(false)
	}
	if !strings.EqualFold(input.DisplayAspectRatio, AspectRatio("", intPointer(input.Width), intPointer(input.Height))) {
		return pointer(true)
	}
	return pointer(false)
}

func nearSquarePixelSAR(value string) bool {
	numerator, denominator, ok := parseFloatRatio(value)
	return ok && denominator > 0 && closeTo(numerator/denominator, 1, .01)
}

func parseFloatRatio(value string) (float64, float64, bool) {
	parts := strings.Split(value, ":")
	if len(parts) != 2 {
		return 0, 0, false
	}
	numerator, errN := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	denominator, errD := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
	if errN != nil || errD != nil || math.IsNaN(numerator) || math.IsNaN(denominator) || math.IsInf(numerator, 0) || math.IsInf(denominator, 0) {
		return 0, 0, false
	}
	return numerator, denominator, true
}

func needsAspectDimensions(value string) bool {
	numerator, denominator, ok := parseRatio(value)
	return value == "0:1" || !ok || numerator <= 0 || denominator <= 0
}

func normalizeSubtitleCodec(codec string) string {
	switch {
	case strings.EqualFold(codec, "dvb_subtitle"):
		return "DVBSUB"
	case strings.EqualFold(codec, "dvb_teletext"):
		return "DVBTXT"
	case strings.EqualFold(codec, "dvd_subtitle"):
		return "DVDSUB"
	case strings.EqualFold(codec, "hdmv_pgs_subtitle"):
		return "PGSSUB"
	default:
		return codec
	}
}

func parseChannelLayout(value string) string {
	if index := strings.IndexByte(value, '('); index >= 0 {
		return value[:index]
	}
	return value
}

func bitDepth(input stream) *int {
	if input.BitsPerSample > 0 {
		return pointer(int(input.BitsPerSample))
	}
	if input.BitsPerRawSample > 0 {
		return pointer(int(input.BitsPerRawSample))
	}
	switch strings.ToLower(input.PixelFormat) {
	case "yuv420p", "yuv444p":
		return pointer(8)
	case "yuv420p10le", "yuv444p10le":
		return pointer(10)
	case "yuv420p12le", "yuv444p12le":
		return pointer(12)
	default:
		return nil
	}
}

func fallbackTitle(title *string, tags tags, defaultHandler string) *string {
	if title != nil && *title != "" {
		return title
	}
	if name := tag(tags, "name"); name != "" {
		return optionalString(name)
	}
	if handler := tag(tags, "handler_name"); handler != "" && !strings.EqualFold(handler, defaultHandler) {
		return optionalString(handler)
	}
	return nil
}

func bitrateFromTags(input stream) (*int, error) {
	if value := parsePositiveInt(tag(input.Tags, "BPS-eng")); value != nil {
		return value, nil
	}
	if value := parsePositiveInt(tag(input.Tags, "BPS")); value != nil {
		return value, nil
	}
	duration, ok := durationFromTag(tag(input.Tags, "DURATION-eng"))
	if !ok {
		duration, ok = durationFromTag(tag(input.Tags, "DURATION"))
	}
	bytes := parseInt64(tag(input.Tags, "NUMBER_OF_BYTES-eng"))
	if bytes == nil {
		bytes = parseInt64(tag(input.Tags, "NUMBER_OF_BYTES"))
	}
	if !ok || duration < 1 || bytes == nil {
		return nil, nil
	}
	value := float64(*bytes) * 8 / duration
	if value > float64(math.MaxInt32) || value < float64(math.MinInt32) {
		return nil, fmt.Errorf("converting tag-derived bitrate to Int32: %g is outside range", value)
	}
	converted := int(math.RoundToEven(value))
	if converted > 0 {
		return &converted, nil
	}
	return nil, nil
}

func normalizeMetadata(info *MediaInfo, result Result, isAudio bool) {
	tags := map[string]string{}
	streamKind := "video"
	if isAudio {
		streamKind = "audio"
	}
	for _, stream := range result.Streams {
		if strings.EqualFold(stream.CodecType, streamKind) {
			mergeTags(tags, stream.Tags)
			break
		}
	}
	if result.Format != nil {
		mergeTags(tags, result.Format.Tags)
	}
	info.Name = optionalString(firstTag(tags, "title", "title-eng"))
	info.ForcedSortName = optionalString(firstTag(tags, "sort_name", "title-sort", "titlesort"))
	info.Overview = optionalString(firstTag(tags, "synopsis", "description", "desc", "comment"))
	info.ShowName = optionalString(firstTag(tags, "show_name", "show"))
	info.Album = optionalString(firstTag(tags, "album"))
	info.Artists = commonArtists(tags)
	if isAudio {
		info.AlbumArtists = splitArtists(firstTag(tags, "albumartist", "album artist", "album_artist"), "/;|\\", true)
		if len(info.AlbumArtists) == 0 {
			info.AlbumArtists = append([]string(nil), info.Artists...)
		}
		info.ParentIndexNumber = parseTrackIndex(firstTag(tags, "disc"))
		info.IndexNumber = parseTrackIndex(firstTag(tags, "track"))
		mineAudioTags(info, tags)
	} else {
		info.ParentIndexNumber = parseInteger(firstTag(tags, "season_number"))
		info.IndexNumber = parseInteger(firstTag(tags, "episode_sort", "episode_id"))
		mineVideoTags(info, tags)
	}
	info.ProductionYear = parseInteger(firstTag(tags, "date"))
	info.PremiereDate = premiereDate(tags)
	if info.ProductionYear == nil && info.PremiereDate != nil {
		info.ProductionYear = pointer(info.PremiereDate.Year())
	}
	if genres := firstTag(tags, "genre"); genres != "" {
		info.Genres = splitTags(genres)
	}
	if !isAudio {
		formatTags := map[string]string{}
		if result.Format != nil {
			mergeTags(formatTags, result.Format.Tags)
		}
		mineWTVTags(info, formatTags)
	}
}

func normalizeChapters(info *MediaInfo, chapters []chapter) error {
	for _, source := range chapters {
		chapter := Chapter{Name: optionalString(exactTag(source.Tags, "title"))}
		if endpoint, ok := chapterTimestampEndpoint(source.StartTime); ok {
			chapter.StartPositionTicks = endpoint
		} else if seconds, err := strconv.ParseFloat(strings.TrimSpace(source.StartTime), 64); err == nil {
			ticks := seconds * 10_000_000
			if math.IsNaN(seconds) || math.IsInf(seconds, 0) || ticks > float64(math.MaxInt64) || ticks < float64(math.MinInt64) {
				return fmt.Errorf("parsing ffprobe chapter start %q: exceeds TimeSpan range", source.StartTime)
			}
			milliseconds := math.RoundToEven(float64(int64(ticks)) / 10_000)
			if milliseconds > float64(math.MaxInt64/10_000) || milliseconds < float64(math.MinInt64/10_000) {
				return fmt.Errorf("parsing ffprobe chapter start %q: exceeds TimeSpan range", source.StartTime)
			}
			chapter.StartPositionTicks = int64(milliseconds) * 10_000
		}
		info.Chapters = append(info.Chapters, chapter)
	}
	return nil
}

func chapterTimestampEndpoint(value string) (int64, bool) {
	seconds, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) {
		return 0, false
	}
	switch seconds * 10_000_000 {
	case float64(math.MaxInt64):
		return (math.MaxInt64 / 10_000) * 10_000, true
	case float64(math.MinInt64):
		return (math.MinInt64 / 10_000) * 10_000, true
	default:
		return 0, false
	}
}

func applyTimestamp(ctx context.Context, info *MediaInfo, options Options) error {
	if info.VideoType == nil || *info.VideoType != playback.VideoTypeVideo || info.Protocol != playback.MediaProtocolFile || !isTransportStream(info.Container) || options.Path == "" {
		return nil
	}
	timestamp, err := MpegTimestamp(ctx, options.Path)
	if err == nil {
		info.Timestamp = &timestamp
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return nil
}

func premiereDate(tags map[string]string) *time.Time {
	for _, key := range []string{"originaldate", "retaildate", "retail date", "retail_date", "date_released", "date", "creation_time"} {
		value := strings.TrimSpace(firstTag(tags, key))
		if value == "" {
			continue
		}
		if len(value) == 4 {
			if year, err := strconv.Atoi(value); err == nil {
				result := time.Date(year, time.January, 1, 0, 0, 0, 0, time.UTC)
				return &result
			}
		}
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05 -07:00", "2006-01-02 15:04:05Z07:00", "2006-01-02 15:04:05", "2006-01-02"} {
			if parsed, err := time.Parse(layout, value); err == nil {
				result := parsed.UTC()
				return &result
			}
		}
	}
	return nil
}

func estimateMissingBitrates(info *MediaInfo) {
	// Retain whether ffprobe supplied any elementary-stream rate before audio
	// estimation. Stock's persisted mapping treats a format-only rate as the
	// video rate in the otherwise-rate-less case; a real stream/tag rate must
	// continue through the ordinary container-minus-other-streams path.
	allEmbeddedBitratesMissing := true
	for index := range info.MediaStreams {
		stream := &info.MediaStreams[index]
		if !stream.IsExternal && stream.BitRate != nil {
			allEmbeddedBitratesMissing = false
			break
		}
	}
	for index := range info.MediaStreams {
		stream := &info.MediaStreams[index]
		if stream.Type == playback.StreamTypeAudio && stream.BitRate == nil {
			stream.BitRate = estimatedAudioBitrate(stream.Codec, derefString(stream.Profile), stream.Channels)
		}
	}
	var video *playback.MediaStream
	videoCount := 0
	for index := range info.MediaStreams {
		if info.MediaStreams[index].Type == playback.StreamTypeVideo {
			video = &info.MediaStreams[index]
			videoCount++
		}
	}
	if info.Bitrate != nil && videoCount == 1 && video.BitRate == nil {
		otherBitrate := 0
		audioBitratesKnown := true
		for index := range info.MediaStreams {
			stream := &info.MediaStreams[index]
			if stream.Type == playback.StreamTypeVideo || stream.IsExternal {
				continue
			}
			if stream.Type == playback.StreamTypeAudio && stream.BitRate == nil {
				audioBitratesKnown = false
				break
			}
			if stream.BitRate != nil {
				otherBitrate += *stream.BitRate
			}
		}
		if audioBitratesKnown {
			if allEmbeddedBitratesMissing && info.Container == "mkv" {
				// Stock's scanner maps a known format-only rate to the sole video
				// stream, then persists the aggregate inferred from those streams.
				video.BitRate = info.Bitrate
				info.Bitrate = pointer(*info.Bitrate + otherBitrate)
			} else if *info.Bitrate-otherBitrate > 0 {
				video.BitRate = pointer(*info.Bitrate - otherBitrate)
			}
		}
	}
	if info.Bitrate == nil && videoCount > 0 {
		total := 0
		for index := range info.MediaStreams {
			stream := &info.MediaStreams[index]
			if !stream.IsExternal && stream.BitRate != nil {
				total += *stream.BitRate
			}
		}
		if total > 0 {
			info.Bitrate = pointer(total)
		}
	}
}

func commonArtists(tags map[string]string) []string {
	if plural := firstTag(tags, "artists"); plural != "" {
		return splitArtists(plural, "/;", false)
	}
	return splitArtists(firstTag(tags, "artist"), "/;,|", true)
}

var performerPattern = regexp.MustCompile(`(.*) \((.*)\)`)

func mineAudioTags(info *MediaInfo, tags map[string]string) {
	for _, credit := range []struct {
		tag  string
		kind string
	}{
		{"composer", "Composer"},
		{"conductor", "Conductor"},
		{"lyricist", "Lyricist"},
		{"performer", ""},
		{"writer", "Writer"},
		{"arranger", "Arranger"},
		{"engineer", "Engineer"},
		{"mixer", "Mixer"},
		{"remixer", "Remixer"},
	} {
		if credit.tag == "performer" {
			for _, performer := range splitMetadataValues(firstTag(tags, credit.tag), false) {
				match := performerPattern.FindStringSubmatch(performer)
				if len(match) != 3 || strings.TrimSpace(match[1]) == "" || strings.TrimSpace(match[2]) == "" {
					continue
				}
				role := titleWords(strings.TrimSpace(match[2]))
				info.People = append(info.People, MediaPerson{Name: strings.TrimSpace(match[1]), Type: "Actor", Role: &role})
			}
			continue
		}
		for _, name := range splitMetadataValues(firstTag(tags, credit.tag), false) {
			info.People = append(info.People, MediaPerson{Name: name, Type: credit.kind})
		}
	}

	for _, tagName := range []string{"organization", "ensemble", "publisher", "label"} {
		fetchStudios(info, tags, tagName)
	}
	for _, provider := range []struct {
		name string
		tags []string
	}{
		{"MusicBrainzAlbumArtist", []string{"MusicBrainz Album Artist Id", "MUSICBRAINZ_ALBUMARTISTID"}},
		{"MusicBrainzArtist", []string{"MusicBrainz Artist Id", "MUSICBRAINZ_ARTISTID"}},
		{"MusicBrainzAlbum", []string{"MusicBrainz Album Id", "MUSICBRAINZ_ALBUMID"}},
		{"MusicBrainzReleaseGroup", []string{"MusicBrainz Release Group Id", "MUSICBRAINZ_RELEASEGROUPID"}},
		{"MusicBrainzTrack", []string{"MusicBrainz Release Track Id", "MUSICBRAINZ_RELEASETRACKID"}},
	} {
		if id := firstMusicBrainzID(firstTag(tags, provider.tags...)); id != "" {
			info.ProviderIDs[provider.name] = id
		}
	}
}

func mineVideoTags(info *MediaInfo, tags map[string]string) {
	fetchStudios(info, tags, "copyright")
	if rating := firstTag(tags, "iTunEXTC"); rating != "" {
		parts := strings.FieldsFunc(rating, func(char rune) bool { return char == '|' })
		if len(parts) > 1 {
			info.OfficialRating = optionalString(parts[1])
		}
		if len(parts) > 3 {
			info.OfficialRatingDescription = optionalString(parts[3])
		}
	}
	if value := firstTag(tags, "iTunMOVI"); value != "" {
		mineITunesMovie(info, value)
	}
}

func fetchStudios(info *MediaInfo, tags map[string]string, tagName string) {
	value := firstTag(tags, tagName)
	if value == "" {
		return
	}
	studios := make([]string, 0)
	for _, studio := range splitMetadataValues(value, true) {
		if containsFold(info.Artists, studio) || containsFold(info.AlbumArtists, studio) {
			continue
		}
		studios = appendDistinctFold(studios, studio)
	}
	// ProbeResultNormalizer assigns on every present tag, so the last one in
	// organization/ensemble/publisher/label order wins.
	info.Studios = studios
}

func splitMetadataValues(value string, allowComma bool) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	delimiters := "/;|\\"
	if allowComma && !strings.ContainsAny(value, delimiters) {
		delimiters = ","
	}
	result := make([]string, 0)
	for _, part := range strings.FieldsFunc(value, func(char rune) bool {
		return strings.ContainsRune(delimiters, char)
	}) {
		if part = strings.TrimSpace(part); part != "" {
			result = append(result, part)
		}
	}
	return result
}

func firstMusicBrainzID(value string) string {
	for _, part := range strings.Split(value, "/") {
		if part = strings.TrimSpace(part); part != "" {
			return part
		}
	}
	return ""
}

func containsFold(values []string, wanted string) bool {
	for _, value := range values {
		if strings.EqualFold(value, wanted) {
			return true
		}
	}
	return false
}

func appendDistinctFold(values []string, value string) []string {
	if value == "" || containsFold(values, value) {
		return values
	}
	return append(values, value)
}

func titleWords(value string) string {
	words := strings.Fields(value)
	for index, word := range words {
		if word == "" || word == strings.ToUpper(word) {
			continue
		}
		runes := []rune(strings.ToLower(word))
		runes[0] = unicode.ToTitle(runes[0])
		words[index] = string(runes)
	}
	return strings.Join(words, " ")
}

type plistValue struct {
	text  string
	array []plistValue
	dict  map[string]plistValue
	order []plistPair
}

type plistPair struct {
	key   string
	value plistValue
}

func mineITunesMovie(info *MediaInfo, value string) {
	start := strings.Index(strings.ToLower(value), "<plist")
	if start < 0 {
		return
	}
	decoder := xml.NewDecoder(strings.NewReader(value[start:]))
	decoder.Strict = false
	root, err := decodePlist(decoder)
	if err != nil || root.dict == nil {
		return
	}
	for index, pair := range root.order {
		// ReadFromDictNode processes a key only when it encounters the next key.
		// the final plist key is therefore observably ignored by pinned stock.
		if index == len(root.order)-1 {
			break
		}
		values := distinctFold(plistStrings(pair.value))
		people := make([]MediaPerson, 0)
		switch pair.key {
		case "studio":
			info.Studios = values
		case "screenwriters", "producers", "directors":
			kind := map[string]string{
				"screenwriters": "Writer",
				"producers":     "Producer",
				"directors":     "Director",
			}[pair.key]
			for _, name := range values {
				people = append(people, MediaPerson{Name: name, Type: kind})
			}
		}
		// Stock ProcessPairs replaces People for every plist key, including
		// keys it does not otherwise recognize.
		info.People = people
	}
}

func decodePlist(decoder *xml.Decoder) (plistValue, error) {
	for {
		token, err := decoder.Token()
		if err != nil {
			return plistValue{}, err
		}
		start, ok := token.(xml.StartElement)
		if !ok {
			continue
		}
		if strings.EqualFold(start.Name.Local, "plist") {
			for {
				token, err := decoder.Token()
				if err != nil {
					return plistValue{}, err
				}
				if child, ok := token.(xml.StartElement); ok {
					return decodePlistValue(decoder, child)
				}
			}
		}
	}
}

func decodePlistValue(decoder *xml.Decoder, start xml.StartElement) (plistValue, error) {
	switch strings.ToLower(start.Name.Local) {
	case "string", "key":
		var text string
		if err := decoder.DecodeElement(&text, &start); err != nil {
			return plistValue{}, err
		}
		return plistValue{text: strings.TrimSpace(text)}, nil
	case "array":
		result := plistValue{array: []plistValue{}}
		for {
			token, err := decoder.Token()
			if err != nil {
				return plistValue{}, err
			}
			switch value := token.(type) {
			case xml.StartElement:
				child, err := decodePlistValue(decoder, value)
				if err != nil {
					return plistValue{}, err
				}
				result.array = append(result.array, child)
			case xml.EndElement:
				if value.Name == start.Name {
					return result, nil
				}
			}
		}
	case "dict":
		result := plistValue{dict: make(map[string]plistValue)}
		key := ""
		for {
			token, err := decoder.Token()
			if err != nil {
				return plistValue{}, err
			}
			switch value := token.(type) {
			case xml.StartElement:
				child, err := decodePlistValue(decoder, value)
				if err != nil {
					return plistValue{}, err
				}
				if strings.EqualFold(value.Name.Local, "key") {
					key = strings.ToLower(child.text)
				} else if key != "" {
					result.dict[key] = child
					result.order = append(result.order, plistPair{key: key, value: child})
					key = ""
				}
			case xml.EndElement:
				if value.Name == start.Name {
					return result, nil
				}
			}
		}
	default:
		if err := decoder.Skip(); err != nil {
			return plistValue{}, err
		}
		return plistValue{}, nil
	}
}

func plistStrings(value plistValue) []string {
	if value.text != "" {
		return []string{value.text}
	}
	result := make([]string, 0)
	for _, entry := range value.array {
		if count := len(entry.order); count > 0 {
			last := entry.order[count-1]
			if last.key != "" && last.value.text != "" {
				result = append(result, last.value.text)
			}
			continue
		}
		result = append(result, plistStrings(entry)...)
	}
	return result
}

func distinctFold(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			result = appendDistinctFold(result, value)
		}
	}
	return result
}

func mineWTVTags(info *MediaInfo, tags map[string]string) {
	if value := firstTag(tags, "WM/Genre"); value != "" {
		genres := distinctFold(strings.FieldsFunc(value, func(char rune) bool {
			return strings.ContainsRune("/;,", char)
		}))
		if len(genres) > 0 {
			info.Genres = genres
		}
	}
	if value := firstTag(tags, "WM/ParentalRating"); value != "" {
		info.OfficialRating = optionalString(value)
	}
	if value := firstTag(tags, "WM/MediaCredits"); value != "" {
		info.People = make([]MediaPerson, 0)
		for _, name := range strings.FieldsFunc(value, func(char rune) bool {
			return char == '/' || char == ';'
		}) {
			if name = strings.TrimSpace(name); name != "" {
				info.People = append(info.People, MediaPerson{Name: name, Type: "Actor"})
			}
		}
	}
	yearText := firstTag(tags, "WM/OriginalReleaseTime")
	if year := parseInteger(yearText); year != nil {
		info.ProductionYear = year
	}
	// Pinned Jellyfin parses the original-release value here (despite reading
	// WM/MediaOriginalBroadcastDateTime immediately above it).
	if _, present := tags["wm/mediaoriginalbroadcastdatetime"]; present {
		if parsed := parseFlexibleDate(yearText); parsed != nil {
			info.PremiereDate = parsed
		}
	}
	description := firstTag(tags, "WM/SubTitleDescription")
	subtitle := firstTag(tags, "WM/SubTitle")
	if subtitle == "" && description != "" {
		prefix := description
		if len(prefix) > 100 {
			prefix = prefix[:100]
		}
		if strings.Contains(prefix, ":") {
			before, _, _ := strings.Cut(description, ":")
			switch {
			case strings.Contains(before, "/"):
				parts := strings.Fields(before)
				if len(parts) > 0 {
					numbers := strings.Split(strings.ReplaceAll(parts[0], ".", ""), "/")
					if len(numbers) > 0 && parseInteger(numbers[0]) != nil {
						info.IndexNumber = parseInteger(numbers[0])
						description = strings.TrimSpace(strings.Join(parts[1:], " "))
					} else if strings.Contains(before, ".") {
						_, description, _ = strings.Cut(before, ".")
						description = strings.TrimSpace(description)
					} else {
						description = strings.TrimSpace(before)
					}
				}
			case strings.Contains(before, "."):
				_, description, _ = strings.Cut(before, ".")
				description = strings.TrimSpace(description)
			default:
				description = strings.TrimSpace(before)
			}
		}
	}
	if strings.TrimSpace(description) != "" {
		info.Overview = optionalString(description)
	}
}

func parseFlexibleDate(value string) *time.Time {
	value = strings.TrimSpace(value)
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05 -07:00", "2006-01-02", "2006"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			parsed = parsed.UTC()
			return &parsed
		}
	}
	return nil
}

func parseTrackIndex(value string) *int {
	value = strings.TrimSpace(value)
	if separator := strings.IndexByte(value, '/'); separator >= 0 {
		value = value[:separator]
	}
	return parsePositiveInt(value)
}

func splitArtists(value, delimiters string, splitFeaturing bool) []string {
	if splitFeaturing {
		value = replaceFold(value, " featuring ", " | ")
		value = replaceFold(value, " feat. ", " | ")
	}
	result := make([]string, 0)
	for _, artist := range artistSplitWhitelist() {
		updated := replaceFold(value, artist, "|")
		if updated != value {
			result = append(result, artist)
			value = updated
		}
	}
	seen := make(map[string]struct{}, len(result))
	for _, artist := range result {
		seen[strings.ToLower(artist)] = struct{}{}
	}
	for _, artist := range strings.FieldsFunc(value, func(r rune) bool { return strings.ContainsRune(delimiters, r) }) {
		artist = strings.TrimSpace(artist)
		if artist == "" {
			continue
		}
		key := strings.ToLower(artist)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, artist)
	}
	return result
}

func replaceFold(value, old, replacement string) string {
	lowerValue, lowerOld := strings.ToLower(value), strings.ToLower(old)
	for start := 0; ; {
		index := strings.Index(lowerValue[start:], lowerOld)
		if index < 0 {
			return value
		}
		index += start
		value = value[:index] + replacement + value[index+len(old):]
		lowerValue = strings.ToLower(value)
		start = index + len(replacement)
	}
}

func artistSplitWhitelist() [30]string {
	return [...]string{"AC/DC", "A/T/O/S", "As/Hi Soundworks", "Au/Ra", "Bremer/McCoy", "b/bqスタヂオ", "DOV/S", "DJ'TEKINA//SOMETHING", "IX/ON", "J-CORE SLi//CER", "M(a/u)SH", "Kaoru/Brilliance", "signum/ii", "Richiter(LORB/DUGEM DI BARAT)", "이달의 소녀 1/3", "R!N / Gemie", "LOONA 1/3", "LOONA / yyxy", "LOONA / ODD EYE CIRCLE", "K/DA", "22/7", "諭吉佳作/men", "//dARTH nULL", "Phantom/Ghost", "She/Her/Hers", "5/8erl in Ehr'n", "Smith/Kotzen", "We;Na", "LSR/CITY", "Kairon; IRSE!"}
}

func estimatedAudioBitrate(codec, profile string, channels *int) *int {
	if channels == nil || *channels < 1 {
		return nil
	}
	multichannel := *channels > 2
	var value int
	switch strings.ToLower(codec) {
	case "aac", "mp3", "mp2":
		if multichannel {
			value = 320000
		} else {
			value = 192000
		}
	case "ac3", "eac3":
		if multichannel {
			value = 640000
		} else {
			value = 192000
		}
	case "dts", "dca":
		if strings.Contains(strings.ToLower(profile), "hd ma") {
			value = *channels * 700000
		} else if multichannel {
			value = 1509000
		} else {
			value = 768000
		}
	case "opus":
		if multichannel {
			value = 256000
		} else {
			value = 128000
		}
	case "vorbis":
		if multichannel {
			value = 320000
		} else {
			value = 160000
		}
	case "wmav1", "wmav2", "wmapro":
		if multichannel {
			value = 384000
		} else {
			value = 192000
		}
	case "flac", "alac":
		value = *channels * 480000
	case "truehd", "mlp":
		value = *channels * 700000
	default:
		return nil
	}
	return &value
}

func parseProtocol(value string) (playback.MediaProtocol, error) {
	switch strings.ToLower(value) {
	case "", "file":
		return playback.MediaProtocolFile, nil
	case "http":
		return playback.MediaProtocolHTTP, nil
	case "rtmp":
		return playback.MediaProtocolRTMP, nil
	case "rtsp":
		return playback.MediaProtocolRTSP, nil
	case "udp":
		return playback.MediaProtocolUDP, nil
	case "rtp":
		return playback.MediaProtocolRTP, nil
	case "ftp":
		return playback.MediaProtocolFTP, nil
	default:
		return playback.MediaProtocolFile, fmt.Errorf("invalid media protocol %q", value)
	}
}

func parseVideoType(value string) (*playback.VideoType, error) {
	var result playback.VideoType
	switch strings.ToLower(value) {
	case "":
		return nil, nil
	case "videofile":
		result = playback.VideoTypeVideo
	case "iso":
		result = playback.VideoTypeIso
	case "dvd":
		result = playback.VideoTypeDVD
	case "bluray":
		result = playback.VideoTypeBluRay
	default:
		return nil, fmt.Errorf("invalid video type %q", value)
	}
	return &result, nil
}

func tag(tags tags, key string) string {
	for candidate, value := range tags {
		if strings.EqualFold(candidate, key) && value != nil {
			return *value
		}
	}
	return ""
}

func mergeTags(destination map[string]string, source tags) {
	for key, value := range source {
		normalized := strings.ToLower(key)
		if value == nil {
			delete(destination, normalized)
		} else {
			destination[normalized] = *value
		}
	}
}

func exactTag(tags chapterTags, key string) string {
	if value, ok := tags[key]; ok && value != nil {
		return *value
	}
	return ""
}

func firstTag(tags map[string]string, keys ...string) string {
	for _, key := range keys {
		if value := tags[strings.ToLower(key)]; strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func splitTags(value string) []string {
	delimiters := "/;|\\"
	if !strings.ContainsAny(value, delimiters) {
		delimiters = ","
	}
	seen := make(map[string]struct{})
	result := make([]string, 0)
	for _, part := range strings.FieldsFunc(value, func(r rune) bool { return strings.ContainsRune(delimiters, r) }) {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		key := strings.ToLower(part)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, part)
	}
	return result
}

func parseInteger(value string) *int {
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 32)
	if err != nil {
		return nil
	}
	result := int(parsed)
	return &result
}

func parsePositiveInt(value string) *int {
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 32)
	if err != nil || parsed <= 0 {
		return nil
	}
	result := int(parsed)
	return &result
}

func parseInt64(value string) *int64 {
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil {
		return nil
	}
	return &parsed
}

func parseFormatSize(value string) (*int64, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil {
		return nil, fmt.Errorf("parsing ffprobe format size %q: %w", value, err)
	}
	return &parsed, nil
}

func parseFormatDuration(value string) (*int64, error) {
	seconds, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) {
		if err == nil {
			err = fmt.Errorf("not a finite number")
		}
		return nil, fmt.Errorf("parsing ffprobe format duration %q: %w", value, err)
	}
	ticks := seconds * 10_000_000
	if ticks > float64(math.MaxInt64) || ticks < float64(math.MinInt64) {
		return nil, fmt.Errorf("parsing ffprobe format duration %q: exceeds TimeSpan range", value)
	}
	if ticks == float64(math.MaxInt64) {
		return pointer(int64(math.MaxInt64)), nil
	}
	result := int64(ticks)
	return &result, nil
}

// correctMP3GaplessTicks applies packet-derived encoder delay and end padding
// only when the selected duration still describes the untrimmed MP3 packets.
// Newer ffprobe versions already expose the trimmed duration; their selected
// duration therefore does not match the packet aggregate and is left intact.
func correctMP3GaplessTicks(runtime *int64, result Result) *int64 {
	facts := result.MP3Gapless
	if runtime == nil || facts == nil || facts.UntrimmedTicks <= 0 || facts.SampleRate <= 0 || facts.SkipSamples < 0 || facts.DiscardPadding < 0 {
		return runtime
	}
	if result.Format == nil || !isMP3Format(result.Format.FormatName) {
		return runtime
	}
	foundMP3Audio := false
	for _, stream := range result.Streams {
		if !strings.EqualFold(stream.CodecType, "audio") {
			continue
		}
		if !strings.EqualFold(stream.CodecName, "mp3") {
			return runtime
		}
		foundMP3Audio = true
		break
	}
	if !foundMP3Audio {
		return runtime
	}
	if math.Abs(float64(*runtime)-float64(facts.UntrimmedTicks)) > 10 {
		return runtime
	}
	if facts.SkipSamples > math.MaxInt64-facts.DiscardPadding {
		return runtime
	}
	padding := facts.SkipSamples + facts.DiscardPadding
	if padding == 0 {
		return runtime
	}
	corrected := float64(facts.UntrimmedTicks) - float64(padding)*10_000_000/float64(facts.SampleRate)
	if math.IsNaN(corrected) || math.IsInf(corrected, 0) || corrected < 0 || corrected > float64(math.MaxInt64) {
		return runtime
	}
	return pointer(int64(math.Round(corrected)))
}

func isMP3Format(value string) bool {
	for _, format := range strings.Split(value, ",") {
		if strings.EqualFold(strings.TrimSpace(format), "mp3") {
			return true
		}
	}
	return false
}

func durationFromTag(value string) (float64, bool) {
	parts := strings.Split(strings.TrimSpace(value), ":")
	if len(parts) != 3 {
		return 0, false
	}
	days, hours := 0, 0
	hourPart, hadDayDot := parts[0], false
	if dot := strings.IndexByte(hourPart, '.'); dot >= 0 {
		var err error
		days, err = strconv.Atoi(hourPart[:dot])
		if err != nil {
			return 0, false
		}
		hourPart, hadDayDot = hourPart[dot+1:], true
	}
	var err error
	hours, err = strconv.Atoi(hourPart)
	if err != nil {
		return 0, false
	}
	if !hadDayDot && hours > 23 {
		days, hours = hours, 0
	}
	if hadDayDot && (hours < 0 || hours > 23) {
		return 0, false
	}
	minutes, err := strconv.Atoi(parts[1])
	if err != nil || minutes < 0 || minutes >= 60 {
		return 0, false
	}
	secondPart := parts[2]
	if dot := strings.IndexByte(secondPart, '.'); dot >= 0 {
		fractionEnd := dot + 1
		for fractionEnd < len(secondPart) && secondPart[fractionEnd] >= '0' && secondPart[fractionEnd] <= '9' {
			fractionEnd++
		}
		if fractionEnd > dot+8 {
			secondPart = secondPart[:dot+8] + secondPart[fractionEnd:]
		}
	}
	seconds, err := strconv.ParseFloat(secondPart, 64)
	if err != nil || seconds < 0 || seconds >= 60 {
		return 0, false
	}
	totalSeconds := float64(days)*24*3600 + float64(hours)*3600 + float64(minutes)*60 + seconds
	if math.IsNaN(totalSeconds) || math.IsInf(totalSeconds, 0) || totalSeconds < 0 || totalSeconds > float64(math.MaxInt64)/10_000_000 {
		return 0, false
	}
	return totalSeconds, true
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func nonBlank(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	return value
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func intPointer(value *int32Value) *int {
	if value == nil {
		return nil
	}
	return pointer(int(*value))
}

func levelPointer(value *int32Value) *float64 {
	if value == nil {
		return nil
	}
	return pointer(float64(*value))
}

func parseRatio(value string) (int, int, bool) {
	parts := strings.Split(value, ":")
	if len(parts) != 2 {
		return 0, 0, false
	}
	n, errN := strconv.ParseInt(strings.TrimSpace(parts[0]), 10, 32)
	d, errD := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 32)
	return int(n), int(d), errN == nil && errD == nil
}
func closeTo(first, second, variance float64) bool { return math.Abs(first-second) <= variance }
func equalAny(value string, expected ...string) bool {
	for _, candidate := range expected {
		if strings.EqualFold(value, candidate) {
			return true
		}
	}
	return false
}

func isTransportStream(container string) bool {
	return strings.EqualFold(container, "mpeg2ts") || strings.EqualFold(container, "m2ts") || strings.EqualFold(container, "ts")
}
func pointer[T any](value T) *T { return &value }

func boolPointer(value *boolString) *bool {
	if value == nil {
		return nil
	}
	result := bool(*value)
	return &result
}

// SPDX-License-Identifier: GPL-2.0-only

package playback

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/encoding"
	"github.com/deadfin-org/deadfin/internal/hls"
	"github.com/deadfin-org/deadfin/internal/sourcefile"
	"github.com/deadfin-org/deadfin/internal/subtitles"
)

const (
	defaultSubtitleCacheBudget  = 32 << 20
	defaultSubtitleResponseSize = 8 << 20
)

var (
	// ErrSubtitleStreamNotFound reports a missing subtitle stream at a requested index.
	ErrSubtitleStreamNotFound = errors.New("subtitle stream not found")
	// ErrSubtitleInvalid reports a subtitle request that cannot be represented by this service.
	ErrSubtitleInvalid = errors.New("invalid subtitle request")
)

// SubtitleStreamer is the handler-facing service seam for HLS subtitle renditions.
type SubtitleStreamer interface {
	Playlist(context.Context, SubtitlePlaylistRequest) (SubtitleResponse, error)
	Stream(context.Context, SubtitleStreamRequest) (SubtitleResponse, error)
}

// SubtitleSources resolves the selected static source without applying stream negotiation.
type SubtitleSources interface {
	StaticSource(context.Context, dto.UUID, dto.UUID, string) (MediaSourceInfo, error)
}

// SubtitleConverter converts one source subtitle stream to a supported output format.
type SubtitleConverter interface {
	Convert(context.Context, subtitles.ConvertJob) ([]byte, error)
}

// SubtitlePlaylistRequest identifies the source and playlist facts for one rendition.
type SubtitlePlaylistRequest struct {
	UserID               dto.UUID
	ItemID               dto.UUID
	MediaSourceID        string
	Index                int
	SegmentLengthSeconds int
	APIKey               string
}

// SubtitleStreamRequest identifies one subtitle stream and its requested time window.
type SubtitleStreamRequest struct {
	UserID             dto.UUID
	ItemID             dto.UUID
	MediaSourceID      string
	Index              int
	Format             string
	StartPositionTicks int64
	EndPositionTicks   int64
	CopyTimestamps     bool
	AddVTTTimeMap      bool
}

// SubtitleResponse is a subtitle response body. A response that names a
// File streams that file instead of Body; the receiver must close File.
type SubtitleResponse struct {
	ContentType string
	Body        []byte
	File        *os.File
	Size        int64
}

// SubtitleServiceConfig supplies the source selector, request-scoped converter, and bounded cache size.
type SubtitleServiceConfig struct {
	Sources     SubtitleSources
	Converter   SubtitleConverter
	CacheBudget int
	// Extractor writes embedded ASS, SSA, PGS and VobSub streams into
	// ExtractionPath (upstream DataPath/subtitles). Nil disables them.
	Extractor      MediaExtractor
	ExtractionPath string
	// ExtractionTimeout returns EncodingOptions.SubtitleExtractionTimeoutMinutes.
	ExtractionTimeout func() time.Duration
}

// SubtitleService resolves and renders subtitle playlist and stream requests. Its cache is
// private to the service and contains only copied converter output.
type SubtitleService struct {
	sources    SubtitleSources
	converter  SubtitleConverter
	cache      subtitleCache
	extraction *subtitleExtraction
}

// NewSubtitleService constructs a subtitle service. A zero or negative cache budget selects
// the bounded production default; conversion is only required by Stream requests that need it.
func NewSubtitleService(config SubtitleServiceConfig) *SubtitleService {
	budget := config.CacheBudget
	if budget <= 0 {
		budget = defaultSubtitleCacheBudget
	}
	return &SubtitleService{
		sources:   config.Sources,
		converter: config.Converter,
		cache:     newSubtitleCache(budget),
		extraction: &subtitleExtraction{
			root:      config.ExtractionPath,
			extractor: config.Extractor,
			timeout:   config.ExtractionTimeout,
		},
	}
}

// Playlist renders the media source's HLS subtitle rendition playlist. The rendition index is
// intentionally ignored, matching Jellyfin's playlist action.
func (service *SubtitleService) Playlist(ctx context.Context, request SubtitlePlaylistRequest) (SubtitleResponse, error) {
	source, err := service.staticSource(ctx, request.UserID, request.ItemID, request.MediaSourceID)
	if err != nil {
		return SubtitleResponse{}, err
	}

	runtimeTicks := int64(0)
	if source.RunTimeTicks != nil {
		runtimeTicks = *source.RunTimeTicks
	}
	playlist, err := hls.BuildSubtitlePlaylist(hls.SubtitlePlaylistRequest{
		RuntimeTicks:         runtimeTicks,
		SegmentLengthSeconds: request.SegmentLengthSeconds,
		APIKey:               request.APIKey,
	})
	if err != nil {
		return SubtitleResponse{}, fmt.Errorf("%w: %v", ErrSubtitleInvalid, err)
	}
	return SubtitleResponse{ContentType: "application/x-mpegURL", Body: []byte(playlist)}, nil
}

// Stream resolves, converts when needed, windows, and returns one subtitle stream response.
func (service *SubtitleService) Stream(ctx context.Context, request SubtitleStreamRequest) (SubtitleResponse, error) {
	// An end of zero is the established unbounded-window sentinel. For every
	// bounded window, keep both operands nonnegative and ordered so subtitle
	// timestamp shifting cannot underflow.
	if request.StartPositionTicks < 0 || request.EndPositionTicks < 0 || (request.EndPositionTicks != 0 && request.EndPositionTicks < request.StartPositionTicks) {
		return SubtitleResponse{}, ErrSubtitleInvalid
	}

	source, err := service.staticSource(ctx, request.UserID, request.ItemID, request.MediaSourceID)
	if err != nil {
		return SubtitleResponse{}, err
	}

	stream := source.MediaStream(StreamTypeSubtitle, request.Index)
	if stream == nil {
		return SubtitleResponse{}, ErrSubtitleStreamNotFound
	}

	rawFormat := strings.ToLower(request.Format)
	if rawFormat == "js" {
		rawFormat = "json"
	}
	// SubtitleEncoder.GetSubtitles returns the readable file unchanged when it
	// already has the requested format: an extracted embedded ASS, SSA, PGS or
	// VobSub stream, or an external PGS file.
	if rawExtractedSubtitle(*stream) && sameSubtitleFormat(encoding.ExtractableSubtitleFormat(stream.Codec), rawFormat) {
		return service.extractedSubtitle(ctx, source, *stream, rawFormat)
	}
	if stream.IsExternal && stream.IsPgsSubtitleStream() && sameSubtitleFormat(subtitleSourceFormat(*stream), rawFormat) {
		return openSubtitleFile(stream.Path, rawFormat)
	}
	if !stream.IsTextSubtitleStream() {
		return SubtitleResponse{}, fmt.Errorf("%w: stream %d is not text", ErrSubtitleInvalid, request.Index)
	}
	sourceFormat := subtitleSourceFormat(*stream)
	if stream.IsExternal && sameSubtitleFormat(sourceFormat, rawFormat) {
		return service.externalPassthrough(stream.Path, rawFormat, request.AddVTTTimeMap)
	}
	// Keep the lowered raw format for response content type; normalize aliases
	// so the whitelist, cache key, converter output, and windowing see srt/vtt.
	format := subtitles.NormalizeFormat(rawFormat)
	if format != "vtt" && format != "srt" {
		return SubtitleResponse{}, fmt.Errorf("%w: unsupported output format %q", ErrSubtitleInvalid, format)
	}
	if service.converter == nil {
		return SubtitleResponse{}, errors.New("subtitle converter is not configured")
	}

	inputPath, streamIndex := source.Path, ffmpegStreamIndex(source.MediaStreams, stream)
	expected := sourcefile.Expected{
		Size:       source.FileFacts.Size,
		ModifiedAt: source.FileFacts.ModifiedAt,
		Inode:      source.FileFacts.Inode,
		ChangedAt:  source.FileFacts.ChangedAt,
	}
	switch {
	case stream.IsExternal:
		inputPath, streamIndex = stream.Path, -1
		expected = sourcefile.Expected{}
	case extractedEmbeddedSubtitle(*stream) && service.extraction.available(source):
		// SubtitleEncoder.GetReadableFile: one pass extracts every embedded
		// subtitle stream of the file into the cache, and the request
		// converts the small cache file. The first request waits for the
		// pass, up to SubtitleExtractionTimeoutMinutes; later requests and
		// the other streams of the file are cache hits.
		extracted, err := service.extractedFile(ctx, source, *stream)
		if err != nil {
			return SubtitleResponse{}, err
		}
		inputPath, streamIndex = extracted, -1
		expected = sourcefile.Expected{}
	}
	pinned, err := sourcefile.Open(inputPath, expected)
	if err != nil {
		return SubtitleResponse{}, err
	}
	defer func() { _ = pinned.Close() }()

	key := subtitleCacheIdentity(pinned.Fingerprint(), streamIndex, format)
	raw, cacheHit := service.cache.get(key)
	if !cacheHit {
		outputFormat := "srt"
		if format == "vtt" {
			outputFormat = "webvtt"
		}
		raw, err = service.converter.Convert(ctx, subtitles.ConvertJob{
			Source:       pinned.File(),
			StreamIndex:  streamIndex,
			OutputFormat: outputFormat,
		})
		if err != nil {
			return SubtitleResponse{}, err
		}
		if err := verifySubtitleSource(pinned); err != nil {
			return SubtitleResponse{}, err
		}
	} else if err := verifySubtitleSource(pinned); err != nil {
		return SubtitleResponse{}, err
	}

	var body []byte
	if format == "vtt" {
		body, err = subtitles.WindowVTT(raw, request.StartPositionTicks, request.EndPositionTicks, request.CopyTimestamps)
	} else {
		body, err = subtitles.WindowSRT(raw, request.StartPositionTicks, request.EndPositionTicks, request.CopyTimestamps)
	}
	if err != nil {
		return SubtitleResponse{}, err
	}
	if len(body) > defaultSubtitleResponseSize {
		return SubtitleResponse{}, subtitles.ErrOutputTooLarge
	}
	if format == "vtt" && request.AddVTTTimeMap {
		body, err = subtitles.AddVTTTimeMapCapped(body, defaultSubtitleResponseSize)
		if err != nil {
			return SubtitleResponse{}, err
		}
	}
	if !cacheHit && len(raw) <= defaultSubtitleResponseSize {
		service.cache.put(key, raw)
	}
	return SubtitleResponse{ContentType: subtitleContentType(rawFormat), Body: body}, nil
}

func (service *SubtitleService) staticSource(ctx context.Context, userID, itemID dto.UUID, mediaSourceID string) (MediaSourceInfo, error) {
	if service == nil || service.sources == nil {
		return MediaSourceInfo{}, ErrItemNotFound
	}
	return service.sources.StaticSource(ctx, userID, itemID, mediaSourceID)
}

func (service *SubtitleService) externalPassthrough(path, format string, addVTTTimeMap bool) (SubtitleResponse, error) {
	pinned, err := sourcefile.Open(path, sourcefile.Expected{})
	if err != nil {
		return SubtitleResponse{}, err
	}
	defer func() { _ = pinned.Close() }()

	body, err := io.ReadAll(io.LimitReader(pinned.File(), defaultSubtitleResponseSize+1))
	if err != nil {
		return SubtitleResponse{}, err
	}
	if len(body) > defaultSubtitleResponseSize {
		return SubtitleResponse{}, subtitles.ErrOutputTooLarge
	}
	if err := verifySubtitleSource(pinned); err != nil {
		return SubtitleResponse{}, err
	}
	if format == "vtt" && addVTTTimeMap {
		body, err = subtitles.AddVTTTimeMapCapped(body, defaultSubtitleResponseSize)
		if err != nil {
			return SubtitleResponse{}, err
		}
	}
	return SubtitleResponse{ContentType: subtitleContentType(format), Body: body}, nil
}

// extractedFile extracts the embedded streams of source when needed and
// returns the cache file of stream.
func (service *SubtitleService) extractedFile(ctx context.Context, source MediaSourceInfo, stream MediaStream) (string, error) {
	pinned, err := openPinnedSource(source)
	if err != nil {
		return "", err
	}
	defer func() { _ = pinned.Close() }()
	if err := service.extraction.ensure(ctx, source, pinned); err != nil {
		return "", err
	}
	path, err := service.extraction.extractedPath(source, stream)
	if err != nil {
		return "", err
	}
	if info, err := os.Stat(path); err != nil || info.Size() == 0 {
		return "", fmt.Errorf("ffmpeg subtitle extraction failed for %s to %s", source.Path, path)
	}
	return path, nil
}

// extractedSubtitle extracts the embedded streams of source when needed and
// opens the cache file of stream.
func (service *SubtitleService) extractedSubtitle(ctx context.Context, source MediaSourceInfo, stream MediaStream, format string) (SubtitleResponse, error) {
	path, err := service.extractedFile(ctx, source, stream)
	if err != nil {
		return SubtitleResponse{}, err
	}
	return openSubtitleFile(path, format)
}

// openSubtitleFile streams a subtitle file without a size limit; PGS tracks of
// a film are tens of megabytes.
func openSubtitleFile(path, format string) (SubtitleResponse, error) {
	file, err := os.Open(path)
	if err != nil {
		return SubtitleResponse{}, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return SubtitleResponse{}, err
	}
	return SubtitleResponse{ContentType: subtitleContentType(format), File: file, Size: info.Size()}, nil
}

func subtitleSourceFormat(stream MediaStream) string {
	if stream.IsExternal {
		if extension := filepath.Ext(stream.Path); extension != "" {
			return subtitles.NormalizeFormat(strings.TrimPrefix(extension, "."))
		}
	}
	return subtitles.NormalizeFormat(stream.Codec)
}

// sameSubtitleFormat reports whether outputFormat needs no conversion of
// sourceFormat. ASS is a superset of SSA. Deadfin also accepts "sup", the PGS
// file extension, for "pgssub" (D-52).
func sameSubtitleFormat(sourceFormat, outputFormat string) bool {
	sourceFormat, outputFormat = strings.ToLower(sourceFormat), strings.ToLower(outputFormat)
	if IsPgsSubtitleFormat(sourceFormat) {
		return outputFormat == "pgssub" || outputFormat == "sup"
	}
	return sourceFormat == outputFormat || sourceFormat == "ssa" && outputFormat == "ass"
}

func subtitleContentType(format string) string {
	switch format {
	case "vtt":
		return "text/vtt"
	case "srt":
		return "application/x-subrip"
	case "ass", "ssa":
		return "text/x-ssa"
	default:
		return "application/octet-stream"
	}
}

type subtitleCacheKey struct {
	dev, ino    uint64
	size        int64
	mtimeNanos  int64
	ctimeNanos  int64
	streamIndex int
	format      string
}

func subtitleCacheIdentity(fingerprint sourcefile.Fingerprint, streamIndex int, format string) subtitleCacheKey {
	return subtitleCacheKey{
		dev:         fingerprint.Dev,
		ino:         fingerprint.Ino,
		size:        fingerprint.Size,
		mtimeNanos:  fingerprint.MTime.UnixNano(),
		ctimeNanos:  fingerprint.CTime.UnixNano(),
		streamIndex: streamIndex,
		format:      format,
	}
}

func verifySubtitleSource(source *sourcefile.Source) error {
	if err := source.VerifyUnchanged(); err != nil {
		return err
	}
	return source.VerifyPathStillCurrent()
}

type subtitleCache struct {
	mu      sync.Mutex
	budget  int
	used    int
	entries map[subtitleCacheKey]*list.Element
	lru     list.List
}

type subtitleCacheEntry struct {
	key  subtitleCacheKey
	body []byte
}

func newSubtitleCache(budget int) subtitleCache {
	return subtitleCache{budget: budget, entries: make(map[subtitleCacheKey]*list.Element)}
}

func (cache *subtitleCache) get(key subtitleCacheKey) ([]byte, bool) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	element, ok := cache.entries[key]
	if !ok {
		return nil, false
	}
	cache.lru.MoveToFront(element)
	body := element.Value.(subtitleCacheEntry).body
	return append([]byte(nil), body...), true
}

func (cache *subtitleCache) put(key subtitleCacheKey, body []byte) {
	if len(body) > cache.budget {
		return
	}
	owned := append([]byte(nil), body...)
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if old, ok := cache.entries[key]; ok {
		entry := old.Value.(subtitleCacheEntry)
		cache.used -= len(entry.body)
		cache.lru.Remove(old)
		delete(cache.entries, key)
	}
	element := cache.lru.PushFront(subtitleCacheEntry{key: key, body: owned})
	cache.entries[key] = element
	cache.used += len(owned)
	for cache.used > cache.budget {
		oldest := cache.lru.Back()
		if oldest == nil {
			break
		}
		entry := oldest.Value.(subtitleCacheEntry)
		cache.used -= len(entry.body)
		delete(cache.entries, entry.key)
		cache.lru.Remove(oldest)
	}
}

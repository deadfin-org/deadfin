// SPDX-License-Identifier: GPL-2.0-only

package playback

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/encoding"
	"github.com/deadfin-org/deadfin/internal/sourcefile"
)

// defaultSubtitleExtractionTimeout is EncodingOptions.SubtitleExtractionTimeoutMinutes.
const defaultSubtitleExtractionTimeout = 30 * time.Minute

// MediaExtractor runs one ffmpeg extraction job. The source, when not nil, is
// the pinned input that args name with sourcefile.FDPath(3).
type MediaExtractor interface {
	Run(ctx context.Context, args []string, source *sourcefile.Source) error
}

// ErrExtractionUnavailable reports that the source has no on-disk extraction
// cache, as upstream PathManager returns null for a non-GUID media source ID.
var ErrExtractionUnavailable = errors.New("media source has no extraction cache")

const extractorInputFD = 3

// subtitleExtraction owns the on-disk cache of embedded subtitle streams that
// the subtitle route returns unchanged (SubtitleEncoder.ExtractAllExtractableSubtitles).
type subtitleExtraction struct {
	root      string
	extractor MediaExtractor
	timeout   func() time.Duration
	locks     keyedLocks
}

// extractedEmbeddedSubtitle reports whether one extraction pass writes stream
// to the cache (SubtitleEncoder.ExtractAllExtractableSubtitles): every
// embedded text, PGS and VobSub stream that supports external delivery.
func extractedEmbeddedSubtitle(stream MediaStream) bool {
	return !stream.IsExternal && stream.SupportsExternalStream && stream.IsExtractableSubtitleStream()
}

// rawExtractedSubtitle reports whether the route serves the extracted file of
// stream as is. ASS, SSA, PGS and VobSub keep their codec on extraction;
// other text codecs are extracted as SRT and then converted per request.
func rawExtractedSubtitle(stream MediaStream) bool {
	return extractedEmbeddedSubtitle(stream) && encoding.ExtractableSubtitleFormat(stream.Codec) != "srt"
}

// available reports whether source has an extraction cache. A source without
// one (a non-GUID ID, as for Live TV) is converted from the media file.
func (extraction *subtitleExtraction) available(source MediaSourceInfo) bool {
	if extraction == nil || extraction.extractor == nil {
		return false
	}
	_, err := mediaSourceCacheFolder(extraction.root, source.ID)
	return err == nil
}

// extractedPath returns the cache file for stream. The caller must have
// called ensure for the source first; the file is missing when the pass wrote
// the stream empty.
func (extraction *subtitleExtraction) extractedPath(source MediaSourceInfo, stream MediaStream) (string, error) {
	folder, err := mediaSourceCacheFolder(extraction.root, source.ID)
	if err != nil {
		return "", err
	}
	return filepath.Join(folder, strconv.Itoa(stream.Index)+"."+encoding.ExtractableSubtitleExtension(stream.Codec)), nil
}

// ensure extracts, in one ffmpeg pass, every embedded subtitle stream of
// source whose cache file is missing or stale. Like upstream, a running
// extraction does not stop when the client disconnects; only the wait for
// another request's extraction does. A stream that ffmpeg writes empty (a
// track with no cues) gets no cache file; the other outputs of the pass are
// kept, and the caller sees the missing file only for that stream.
func (extraction *subtitleExtraction) ensure(ctx context.Context, source MediaSourceInfo, pinned *sourcefile.Source) error {
	if extraction == nil || extraction.extractor == nil || extraction.root == "" {
		return ErrExtractionUnavailable
	}
	folder, err := mediaSourceCacheFolder(extraction.root, source.ID)
	if err != nil {
		return err
	}
	unlock, err := extraction.locks.lock(ctx, folder)
	if err != nil {
		return err
	}
	defer unlock()

	meta := extractionMeta(pinned.Fingerprint())
	var outputs []encoding.SubtitleExtraction
	for index := range source.MediaStreams {
		stream := &source.MediaStreams[index]
		if !extractedEmbeddedSubtitle(*stream) {
			continue
		}
		path, err := extraction.extractedPath(source, *stream)
		if err != nil {
			return err
		}
		if extractedFresh(path, meta) {
			continue
		}
		inputIndex := embeddedInputIndex(source.MediaStreams, index)
		if inputIndex < 0 {
			continue
		}
		outputs = append(outputs, encoding.SubtitleExtraction{InputIndex: inputIndex, Codec: stream.Codec, OutputPath: path})
	}
	if len(outputs) == 0 {
		return nil
	}
	if err := os.MkdirAll(folder, 0o755); err != nil {
		return err
	}
	for _, output := range outputs {
		if err := os.Remove(output.OutputPath + ".meta"); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}

	timeout := defaultSubtitleExtractionTimeout
	if extraction.timeout != nil {
		if configured := extraction.timeout(); configured > 0 {
			timeout = configured
		}
	}
	runCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()
	runErr := extraction.extractor.Run(runCtx, encoding.SubtitleExtractionArgs(sourcefile.FDPath(extractorInputFD), outputs), pinned)
	if runErr == nil {
		runErr = verifySubtitleSource(pinned)
	}
	if runErr != nil {
		removeOutputs(outputs)
		return fmt.Errorf("ffmpeg subtitle extraction failed for %s: %w", source.Path, runErr)
	}
	for _, output := range outputs {
		info, err := os.Stat(output.OutputPath)
		if err != nil || info.Size() == 0 {
			_ = os.Remove(output.OutputPath)
			continue
		}
		if strings.HasSuffix(output.OutputPath, "ass") {
			if err := setASSFont(output.OutputPath); err != nil {
				return err
			}
		}
		if err := os.WriteFile(output.OutputPath+".meta", []byte(meta), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// setASSFont ports SubtitleEncoder.SetAssFont: styles that name Arial name
// Arial Unicode MS instead.
func setASSFont(path string) error {
	text, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	replaced := bytes.ReplaceAll(text, []byte(",Arial,"), []byte(",Arial Unicode MS,"))
	if bytes.Equal(text, replaced) {
		return nil
	}
	return os.WriteFile(path, replaced, 0o644)
}

func removeOutputs(outputs []encoding.SubtitleExtraction) {
	for _, output := range outputs {
		_ = os.Remove(output.OutputPath)
	}
}

// extractionMeta records the source revision that built a cache file, as
// upstream's "<size>:<mtimeTicks>" sidecar does.
func extractionMeta(fingerprint sourcefile.Fingerprint) string {
	return strconv.FormatInt(fingerprint.Size, 10) + ":" + strconv.FormatInt(fingerprint.MTime.UnixNano(), 10)
}

// extractedFresh ports SubtitleEncoder.IsCachedSubtitleFresh.
func extractedFresh(path, meta string) bool {
	info, err := os.Stat(path)
	if err != nil || info.Size() == 0 {
		return false
	}
	recorded, err := os.ReadFile(path + ".meta")
	return err == nil && string(recorded) == meta
}

// embeddedInputIndex ports EncodingHelper.FindIndex: the ffmpeg input index
// of an embedded stream is the count of earlier streams from the same file.
// External sidecars come first in MediaStreams and have other paths.
func embeddedInputIndex(streams []MediaStream, target int) int {
	if target < 0 || target >= len(streams) {
		return -1
	}
	index := 0
	for position := range streams {
		if position == target {
			return index
		}
		if streams[position].Path == streams[target].Path {
			index++
		}
	}
	return -1
}

// mediaSourceCacheFolder ports PathManager.GetSubtitleFolderPath and
// GetAttachmentFolderPath: <root>/<id[:2]>/<id> with the D-format GUID.
func mediaSourceCacheFolder(root, mediaSourceID string) (string, error) {
	if root == "" {
		return "", ErrExtractionUnavailable
	}
	id, err := dto.ParseUUID(mediaSourceID)
	if err != nil {
		return "", ErrExtractionUnavailable
	}
	dashed := id.Dashed()
	return filepath.Join(root, dashed[:2], dashed), nil
}

// keyedLocks serializes work per key, as upstream AsyncKeyedLocker does. A
// waiter stops waiting when its context ends.
type keyedLocks struct {
	mu    sync.Mutex
	locks map[string]*keyedLock
}

type keyedLock struct {
	token chan struct{}
	refs  int
}

func (locks *keyedLocks) lock(ctx context.Context, key string) (func(), error) {
	locks.mu.Lock()
	if locks.locks == nil {
		locks.locks = make(map[string]*keyedLock)
	}
	entry := locks.locks[key]
	if entry == nil {
		entry = &keyedLock{token: make(chan struct{}, 1)}
		locks.locks[key] = entry
	}
	entry.refs++
	locks.mu.Unlock()

	release := func() {
		locks.mu.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(locks.locks, key)
		}
		locks.mu.Unlock()
	}
	select {
	case entry.token <- struct{}{}:
		return func() {
			<-entry.token
			release()
		}, nil
	case <-ctx.Done():
		release()
		return nil, ctx.Err()
	}
}

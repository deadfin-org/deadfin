// SPDX-License-Identifier: GPL-2.0-only

package imageserve

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/images"
)

const (
	// RemoteFailureTTL is how long RemoteCache remembers a failed download.
	// A request for the same URL in this window gets the remembered error and
	// causes no new fetch (divergence D-127).
	RemoteFailureTTL = time.Hour
	// maxRemoteFailures bounds the remembered failures. When the bound is
	// reached, expired entries go first, then the entry that expires first.
	maxRemoteFailures = 4096
)

var (
	// ErrRemoteGone reports that the remote source answered 404 Not Found or
	// 403 Forbidden. Upstream LibraryManager.ConvertImageToLocal removes the
	// image from the item for these two statuses.
	ErrRemoteGone = errors.New("imageserve: remote image gone")
	// ErrRemoteFailed reports another failed download, or a failure that
	// RemoteCache still remembers.
	ErrRemoteFailed = errors.New("imageserve: remote image download failed")
)

// RemoteStatus is implemented by a Downloader error that carries the HTTP
// status of the remote response.
type RemoteStatus interface {
	RemoteStatus() int
}

// Downloader fetches the raw bytes of a remote image. The concrete
// implementation enforces the remote-image HTTPS, address, size, and content
// checks; this seam keeps imageserve independent and testable.
type Downloader interface {
	Fetch(ctx context.Context, url string) ([]byte, error)
}

// RemoteCache downloads remote provider image URLs into a local cache directory
// and serves the cached copy on every subsequent request, so serving never
// hot-links and never re-downloads a poster that is already on disk. It matches
// upstream's remote-image contract (download once, then serve the resized local
// copy through the normal image pipeline).
//
// A failed download is logged with the host and status only, because a
// provider URL can carry credentials. The failure is remembered per URL for
// RemoteFailureTTL, and concurrent requests for one URL share one fetch, so a
// dead logo on an IPTV panel host is not fetched again on every guide view.
type RemoteCache struct {
	downloader Downloader
	dir        string
	clock      clock.Clock
	logger     *slog.Logger
	fetches    singleflight.Group

	mu       sync.Mutex
	failures map[string]remoteFailure
}

// remoteFailure is one remembered failed download.
type remoteFailure struct {
	err     error
	expires time.Time
}

// RemoteCacheOptions are the optional RemoteCache dependencies. A nil Clock
// uses the real clock; a nil Logger discards the log.
type RemoteCacheOptions struct {
	Clock  clock.Clock
	Logger *slog.Logger
}

// NewRemoteCache constructs a RemoteCache that stores downloads beneath dir.
func NewRemoteCache(downloader Downloader, dir string, options RemoteCacheOptions) *RemoteCache {
	clk := options.Clock
	if clk == nil {
		clk = clock.NewReal()
	}
	logger := options.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &RemoteCache{
		downloader: downloader,
		dir:        dir,
		clock:      clk,
		logger:     logger,
		failures:   map[string]remoteFailure{},
	}
}

// Local returns a local path for url, downloading and caching it on first use.
// The returned modification time is the cached file's, so it stays stable across
// server restarts (the file persists) and feeds the resize cache identity.
//
// A failed download returns an error that wraps ErrRemoteGone or
// ErrRemoteFailed. The same error comes back without a fetch until
// RemoteFailureTTL has passed.
func (cache *RemoteCache) Local(ctx context.Context, url string) (string, time.Time, error) {
	if cache == nil || cache.downloader == nil {
		return "", time.Time{}, ErrRemoteUnavailable
	}
	if err := ctx.Err(); err != nil {
		return "", time.Time{}, err
	}

	path := cache.pathFor(url)
	if info, err := os.Stat(path); err == nil {
		return path, info.ModTime(), nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", time.Time{}, fmt.Errorf("stat remote image cache %q: %w", path, err)
	}

	if err := cache.rememberedFailure(url); err != nil {
		return "", time.Time{}, err
	}

	// Concurrent requests for one URL share one fetch. The fetch does not
	// stop when the first caller goes away: the downloader timeout bounds it,
	// and its result serves every caller.
	_, err, _ := cache.fetches.Do(url, func() (any, error) {
		return nil, cache.download(context.WithoutCancel(ctx), url, path)
	})
	if err != nil {
		return "", time.Time{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("stat published remote image %q: %w", path, err)
	}
	return path, info.ModTime(), nil
}

// download fetches url into path. A failed fetch is logged and remembered.
func (cache *RemoteCache) download(ctx context.Context, url, path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	if err := cache.rememberedFailure(url); err != nil {
		return err
	}
	body, err := cache.downloader.Fetch(ctx, url)
	if err == nil && len(body) == 0 {
		err = errors.New("remote image: empty body")
	}
	if err != nil {
		return cache.fail(url, err)
	}
	return writeAtomically(path, body)
}

// fail logs a failed download and remembers it for RemoteFailureTTL. The log
// names the host and the status, never the full URL.
func (cache *RemoteCache) fail(rawURL string, err error) error {
	status := 0
	var withStatus RemoteStatus
	if errors.As(err, &withStatus) {
		status = withStatus.RemoteStatus()
	}
	kind := ErrRemoteFailed
	if status == http.StatusNotFound || status == http.StatusForbidden {
		kind = ErrRemoteGone
	}
	failure := remoteError{kind: kind, cause: err, reason: failureReason(err, status)}

	attributes := []any{slog.String("host", remoteHost(rawURL))}
	if status != 0 {
		attributes = append(attributes, slog.Int("status", status))
	} else {
		attributes = append(attributes, slog.String("reason", failureReason(err, status)))
	}
	attributes = append(attributes, slog.Duration("retry_after", RemoteFailureTTL))
	cache.logger.Warn("remote image download failed", attributes...)

	now := cache.clock.Now()
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if len(cache.failures) >= maxRemoteFailures {
		cache.evictLocked(now)
	}
	cache.failures[rawURL] = remoteFailure{err: failure, expires: now.Add(RemoteFailureTTL)}
	return failure
}

// rememberedFailure returns the remembered error for url, or nil when there
// is none or it has expired.
func (cache *RemoteCache) rememberedFailure(url string) error {
	now := cache.clock.Now()
	cache.mu.Lock()
	defer cache.mu.Unlock()
	failure, ok := cache.failures[url]
	if !ok {
		return nil
	}
	if !now.Before(failure.expires) {
		delete(cache.failures, url)
		return nil
	}
	return failure.err
}

// evictLocked removes the expired failures. When none has expired, it
// removes the failure that expires first.
func (cache *RemoteCache) evictLocked(now time.Time) {
	oldest := ""
	var oldestExpiry time.Time
	for key, failure := range cache.failures {
		if !now.Before(failure.expires) {
			delete(cache.failures, key)
			continue
		}
		if oldest == "" || failure.expires.Before(oldestExpiry) {
			oldest, oldestExpiry = key, failure.expires
		}
	}
	if len(cache.failures) >= maxRemoteFailures && oldest != "" {
		delete(cache.failures, oldest)
	}
}

// remoteError is a failed download. Its text has no URL; it unwraps to its
// kind (ErrRemoteGone or ErrRemoteFailed) and to the downloader error.
type remoteError struct {
	kind   error
	cause  error
	reason string
}

func (e remoteError) Error() string   { return e.kind.Error() + ": " + e.reason }
func (e remoteError) Unwrap() []error { return []error{e.kind, e.cause} }

// failureReason describes a failed download without the URL. A *url.Error
// names the full URL, so only its inner error is used.
func failureReason(err error, status int) string {
	if status != 0 {
		return fmt.Sprintf("status %d", status)
	}
	var urlError *url.Error
	if errors.As(err, &urlError) {
		return urlError.Err.Error()
	}
	return err.Error()
}

// remoteHost returns the host and port of rawURL, without user information.
func remoteHost(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" {
		return "invalid"
	}
	return parsed.Host
}

// Store writes validated image bytes into the same cache the serving path reads,
// reusing pathFor + writeAtomically so the bytes land at the same on-disk
// location a Local() call would use. It does not download: the caller has already
// fetched and validated the bytes. The item row is updated only after this
// succeeds — a failed write persists nothing.
func (cache *RemoteCache) Store(url string, body []byte) (string, time.Time, error) {
	if cache == nil || len(body) == 0 {
		return "", time.Time{}, ErrRemoteUnavailable
	}
	path := cache.pathFor(url)
	if err := writeAtomically(path, body); err != nil {
		return "", time.Time{}, err
	}
	cache.mu.Lock()
	delete(cache.failures, url)
	cache.mu.Unlock()
	info, err := os.Stat(path)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("stat published remote image %q: %w", path, err)
	}
	return path, info.ModTime(), nil
}

// pathFor derives the on-disk location for url. The MD5GUID digest matches the
// resize cache's fan-out layout (one hex-nibble subdirectory), and the URL's
// extension is preserved so the processor decodes the download natively.
func (cache *RemoteCache) pathFor(url string) string {
	digest := images.MD5GUID(url)
	return filepath.Join(cache.dir, digest[:1], digest+remoteExtension(url))
}

// remoteExtension extracts a decodable image extension from a URL, defaulting to
// .jpg (TMDb and most provider CDNs serve JPEG) when the URL carries none.
func remoteExtension(url string) string {
	trimmed := url
	if index := strings.IndexAny(trimmed, "?#"); index >= 0 {
		trimmed = trimmed[:index]
	}
	switch ext := strings.ToLower(filepath.Ext(trimmed)); ext {
	case ".jpg", ".jpeg", ".png", ".webp", ".gif", ".bmp", ".svg":
		return ext
	default:
		return ".jpg"
	}
}

// writeAtomically publishes body to path via a temporary file and rename, so a
// concurrent reader never observes a partial download.
func writeAtomically(path string, body []byte) (err error) {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return fmt.Errorf("create remote image cache directory %q: %w", directory, err)
	}
	temporary, err := os.CreateTemp(directory, ".deadfin-remote-image-*")
	if err != nil {
		return fmt.Errorf("create temporary remote image file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() {
		if temporary != nil {
			_ = temporary.Close()
		}
		if err != nil {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o644); err != nil {
		return fmt.Errorf("set temporary remote image permissions: %w", err)
	}
	if _, err := temporary.Write(body); err != nil {
		return fmt.Errorf("write temporary remote image file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync temporary remote image file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary remote image file: %w", err)
	}
	temporary = nil
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("publish remote image file: %w", err)
	}
	return nil
}

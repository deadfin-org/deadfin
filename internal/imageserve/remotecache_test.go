// SPDX-License-Identifier: GPL-2.0-only

package imageserve_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	neturl "net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/imageserve"
)

type fakeDownloader struct {
	body  []byte
	err   error
	calls int
}

func (f *fakeDownloader) Fetch(_ context.Context, _ string) ([]byte, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.body, nil
}

func TestRemoteCacheDownloadsOnceThenServesFromDisk(t *testing.T) {
	payload := []byte("\xff\xd8\xff\xe0jpeg-bytes")
	downloader := &fakeDownloader{body: payload}
	cache := imageserve.NewRemoteCache(downloader, t.TempDir(), imageserve.RemoteCacheOptions{})

	const url = "https://image.tmdb.org/t/p/original/abc123.jpg"
	path, modified, err := cache.Local(context.Background(), url)
	if err != nil {
		t.Fatalf("first Local: %v", err)
	}
	if modified.IsZero() {
		t.Error("first Local modified time is zero, want the published file mtime")
	}
	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read published file: %v", err)
	}
	if string(stored) != string(payload) {
		t.Errorf("published bytes = %q, want the downloaded payload", stored)
	}
	if got := path[len(path)-4:]; got != ".jpg" {
		t.Errorf("published path extension = %q, want .jpg preserved from URL", got)
	}

	secondPath, _, err := cache.Local(context.Background(), url)
	if err != nil {
		t.Fatalf("second Local: %v", err)
	}
	if secondPath != path {
		t.Errorf("second Local path = %q, want stable %q", secondPath, path)
	}
	if downloader.calls != 1 {
		t.Errorf("downloader calls = %d, want exactly one download for a cached image", downloader.calls)
	}
}

func TestRemoteCacheDefaultsExtensionWhenURLHasNone(t *testing.T) {
	cache := imageserve.NewRemoteCache(&fakeDownloader{body: []byte("bytes")}, t.TempDir(), imageserve.RemoteCacheOptions{})
	path, _, err := cache.Local(context.Background(), "https://cdn.example/image?size=orig")
	if err != nil {
		t.Fatalf("Local: %v", err)
	}
	if got := path[len(path)-4:]; got != ".jpg" {
		t.Errorf("extension = %q, want default .jpg", got)
	}
}

func TestRemoteCachePropagatesDownloadError(t *testing.T) {
	wantErr := errors.New("boom")
	cache := imageserve.NewRemoteCache(&fakeDownloader{err: wantErr}, t.TempDir(), imageserve.RemoteCacheOptions{})
	if _, _, err := cache.Local(context.Background(), "https://cdn.example/x.jpg"); !errors.Is(err, wantErr) {
		t.Errorf("Local error = %v, want the download error", err)
	}
}

// TestRemoteCacheStoreWritesAndServes verifies Store writes validated bytes
// to the same cache the serving path reads, so a subsequent Local call for
// the same URL serves the stored copy without downloading.
func TestRemoteCacheStoreWritesAndServes(t *testing.T) {
	payload := []byte("\xff\xd8\xff\xe0stored-jpeg")
	downloader := &fakeDownloader{err: errors.New("should not download")}
	cache := imageserve.NewRemoteCache(downloader, t.TempDir(), imageserve.RemoteCacheOptions{})

	const url = "https://image.tmdb.org/t/p/original/poster.jpg"
	path, _, err := cache.Store(url, payload)
	if err != nil {
		t.Fatalf("Store: %v", err)
	}
	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read stored file: %v", err)
	}
	if string(stored) != string(payload) {
		t.Errorf("stored bytes = %q, want %q", stored, payload)
	}

	// A subsequent Local call must serve the stored copy without downloading.
	servedPath, _, err := cache.Local(context.Background(), url)
	if err != nil {
		t.Fatalf("Local after Store: %v", err)
	}
	if servedPath != path {
		t.Errorf("served path = %q, want %q (same as stored)", servedPath, path)
	}
	if downloader.calls != 0 {
		t.Errorf("downloader calls = %d, want 0 (served from Store)", downloader.calls)
	}
}

type statusError int

func (e statusError) Error() string     { return "remote image: status " + strconv.Itoa(int(e)) }
func (e statusError) RemoteStatus() int { return int(e) }

// countingDownloader counts fetches; it is safe for concurrent use.
type countingDownloader struct {
	mu    sync.Mutex
	calls int
	err   error
	gate  chan struct{}
}

func (d *countingDownloader) Fetch(_ context.Context, _ string) ([]byte, error) {
	d.mu.Lock()
	d.calls++
	d.mu.Unlock()
	if d.gate != nil {
		<-d.gate
	}
	return nil, d.err
}

func (d *countingDownloader) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}

func newFailingCache(t *testing.T, err error) (*imageserve.RemoteCache, *countingDownloader, *clock.Fake, *bytes.Buffer) {
	t.Helper()
	downloader := &countingDownloader{err: err}
	clk := clock.NewFake(time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC))
	var logs bytes.Buffer
	cache := imageserve.NewRemoteCache(downloader, t.TempDir(), imageserve.RemoteCacheOptions{
		Clock:  clk,
		Logger: slog.New(slog.NewTextHandler(&logs, nil)),
	})
	return cache, downloader, clk, &logs
}

// TestRemoteCacheRemembersFailure checks #886: a failed download answers
// without a new fetch for RemoteFailureTTL, then fetches again.
func TestRemoteCacheRemembersFailure(t *testing.T) {
	cache, downloader, clk, _ := newFailingCache(t, statusError(http.StatusInternalServerError))
	const url = "https://static.example/logo.png"
	for range 3 {
		if _, _, err := cache.Local(t.Context(), url); !errors.Is(err, imageserve.ErrRemoteFailed) {
			t.Fatalf("Local error = %v, want ErrRemoteFailed", err)
		}
	}
	if got := downloader.count(); got != 1 {
		t.Fatalf("fetches = %d, want 1 inside the failure window", got)
	}
	clk.Advance(imageserve.RemoteFailureTTL - time.Second)
	_, _, _ = cache.Local(t.Context(), url)
	if got := downloader.count(); got != 1 {
		t.Fatalf("fetches = %d one second before expiry, want 1", got)
	}
	clk.Advance(time.Second)
	_, _, _ = cache.Local(t.Context(), url)
	if got := downloader.count(); got != 2 {
		t.Fatalf("fetches = %d after expiry, want 2", got)
	}
}

// TestRemoteCacheClassifiesStatus checks that 404 and 403 are ErrRemoteGone,
// as upstream ConvertImageToLocal removes the image for these two statuses,
// and that other failures are ErrRemoteFailed.
func TestRemoteCacheClassifiesStatus(t *testing.T) {
	for _, test := range []struct {
		err  error
		want error
	}{
		{statusError(http.StatusNotFound), imageserve.ErrRemoteGone},
		{statusError(http.StatusForbidden), imageserve.ErrRemoteGone},
		{statusError(http.StatusGone), imageserve.ErrRemoteFailed},
		{statusError(http.StatusBadGateway), imageserve.ErrRemoteFailed},
		{errors.New("dial timeout"), imageserve.ErrRemoteFailed},
	} {
		cache, _, _, _ := newFailingCache(t, test.err)
		_, _, err := cache.Local(t.Context(), "https://cdn.example/x.jpg")
		if !errors.Is(err, test.want) || !errors.Is(err, test.err) {
			t.Errorf("%v: Local error = %v, want %v wrapping the download error", test.err, err, test.want)
		}
	}
}

// TestRemoteCacheLogsHostAndStatusOnly checks that the log and the error
// name the host and status, never credentials or the URL path.
func TestRemoteCacheLogsHostAndStatusOnly(t *testing.T) {
	const url = "http://panel.example:2086/images/user/secret-token/logo.png"
	for _, cause := range []error{
		statusError(http.StatusNotFound),
		&neturl.Error{Op: "Get", URL: url, Err: errors.New("connection refused")},
	} {
		cache, _, _, logs := newFailingCache(t, cause)
		_, _, err := cache.Local(t.Context(), url)
		text := logs.String()
		if !strings.Contains(text, "host=panel.example:2086") {
			t.Errorf("log %q has no host", text)
		}
		for _, leaked := range []string{"secret-token", "/images/"} {
			if strings.Contains(text, leaked) || strings.Contains(err.Error(), leaked) {
				t.Errorf("log %q or error %q contains %q", text, err, leaked)
			}
		}
	}
	cache, _, _, logs := newFailingCache(t, statusError(http.StatusNotFound))
	_, _, _ = cache.Local(t.Context(), url)
	if !strings.Contains(logs.String(), "status=404") {
		t.Errorf("log %q has no status", logs.String())
	}
}

// TestRemoteCacheSharesConcurrentFetch checks that concurrent requests for a
// failing URL cause one fetch.
func TestRemoteCacheSharesConcurrentFetch(t *testing.T) {
	cache, downloader, _, _ := newFailingCache(t, statusError(http.StatusBadGateway))
	downloader.gate = make(chan struct{})
	const url = "https://static.example/logo.png"
	var group sync.WaitGroup
	for range 8 {
		group.Add(1)
		go func() {
			defer group.Done()
			_, _, _ = cache.Local(t.Context(), url)
		}()
	}
	close(downloader.gate)
	group.Wait()
	if got := downloader.count(); got != 1 {
		t.Fatalf("fetches = %d, want 1", got)
	}
}

// TestRemoteCacheBoundsFailures checks that the failure memory is bounded and
// drops the failure that expires first.
func TestRemoteCacheBoundsFailures(t *testing.T) {
	cache, downloader, clk, _ := newFailingCache(t, statusError(http.StatusBadGateway))
	for index := range imageserve.MaxRemoteFailures + 1 {
		_, _, _ = cache.Local(t.Context(), "https://static.example/"+strconv.Itoa(index)+".png")
		clk.Advance(time.Millisecond)
	}
	before := downloader.count()
	_, _, _ = cache.Local(t.Context(), "https://static.example/1.png")
	if downloader.count() != before {
		t.Fatalf("second-oldest failure was not remembered")
	}
	_, _, _ = cache.Local(t.Context(), "https://static.example/0.png")
	if downloader.count() != before+1 {
		t.Fatalf("oldest failure was not dropped at the bound")
	}
}

// TestRemoteCacheStoreClearsFailure checks that bytes stored by the identify
// path replace a remembered failure.
func TestRemoteCacheStoreClearsFailure(t *testing.T) {
	cache, downloader, _, _ := newFailingCache(t, statusError(http.StatusBadGateway))
	const url = "https://static.example/logo.png"
	_, _, _ = cache.Local(t.Context(), url)
	if _, _, err := cache.Store(url, []byte("png")); err != nil {
		t.Fatalf("Store: %v", err)
	}
	if _, _, err := cache.Local(t.Context(), url); err != nil {
		t.Fatalf("Local after Store: %v", err)
	}
	if got := downloader.count(); got != 1 {
		t.Fatalf("fetches = %d, want 1", got)
	}
}

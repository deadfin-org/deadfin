// SPDX-License-Identifier: GPL-2.0-only

package library_test

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/library"
)

// folderLogRecord is one captured scanner log line.
type folderLogRecord struct {
	message string
	path    string
	status  string
	err     string
}

// channelHandler sends each record to a channel, so a test can wait for a
// log line while the scan that writes it still runs.
type channelHandler struct {
	records chan folderLogRecord
}

func (h channelHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h channelHandler) Handle(_ context.Context, r slog.Record) error {
	record := folderLogRecord{message: r.Message}
	r.Attrs(func(a slog.Attr) bool {
		switch a.Key {
		case "path":
			record.path = a.Value.String()
		case "status":
			record.status = a.Value.String()
		case "error":
			record.err = a.Value.String()
		}
		return true
	})
	h.records <- record
	return nil
}

func (h channelHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h channelHandler) WithGroup(string) slog.Handler      { return h }

// TestScannerLogsFolderFailureBeforeScanReturns proves that a folder failure
// is logged when it occurs, not after the scan drains. A scan that stalls on
// a later folder must still show the earlier failure in the log (#702).
func TestScannerLogsFolderFailureBeforeScanReturns(t *testing.T) {
	resolver := &scannerFixtureResolver{}
	harness := newScannerHarness(t, resolver)
	// The queue emits outcomes in dispatch order. The stalled file is one
	// level deeper, so its folder is dispatched only after both top-level
	// folders, the failed one included, have been emitted.
	bad := scannerPath(t, harness.source, "bad", "movie.mkv")
	stalled := scannerPath(t, harness.source, "slow", "inner", "movie.mkv")
	writeScannerFile(t, bad, "bad")
	writeScannerFile(t, stalled, "stalled")
	if err := harness.scanner.Close(); err != nil {
		t.Fatalf("close default scanner: %v", err)
	}

	resolver.setMalformed(bad)
	release := make(chan struct{})
	reached := make(chan struct{})
	resolver.block = func(ctx context.Context, path string) {
		if path != stalled {
			return
		}
		close(reached)
		select {
		case <-release:
		case <-ctx.Done():
		}
	}
	records := make(chan folderLogRecord, 64)
	harness.scanner = newScanner(t, harness.topology, harness.items, library.NewChain(resolver), library.ScannerOptions{
		Concurrency: 2,
		Logger:      slog.New(channelHandler{records: records}),
	})

	done := make(chan error, 1)
	go func() {
		_, err := harness.scanner.Scan(t.Context())
		done <- err
	}()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	select {
	case <-reached:
	case <-time.After(5 * time.Second):
		t.Fatal("scan did not reach the stalled folder")
	}

	var got folderLogRecord
	deadline := time.After(5 * time.Second)
	for got.message == "" {
		select {
		case record := <-records:
			if record.message == "scanner folder" {
				got = record
			}
		case err := <-done:
			t.Fatalf("scan returned (%v) while the stalled folder was still blocked", err)
		case <-deadline:
			t.Fatal("scanner did not log the failed folder while the scan was still running")
		}
	}
	if got.status != "failed" || got.err == "" {
		t.Fatalf("folder log = %+v, want status failed with an error", got)
	}
	if got.path != filepath.Dir(bad) {
		t.Fatalf("folder log path = %q, want the failed folder %q", got.path, filepath.Dir(bad))
	}
	select {
	case err := <-done:
		t.Fatalf("scan returned (%v) before the stalled folder was released", err)
	default:
	}

	close(release)
	if err := <-done; err != nil {
		t.Fatalf("scan: %v", err)
	}
}

// SPDX-License-Identifier: GPL-2.0-only

package ffmpeg

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/clock"
	"go.uber.org/goleak"
)

// TestHLSCapacityBudgetPausesAndResumesThrottleThroughManager verifies that the
// per-family cache budget pauses write-ahead production when synthetic segments
// of known size cross the configured budget, and resumes when deletion drops
// the family below budget. The test exercises the full manager lifecycle:
// the throttler reads job state under the manager lock, the familyCacheBytes
// closure enumerates the family directory, and the fake ffmpeg records the p
// and u keystrokes it receives on stdin.
func TestHLSCapacityBudgetPausesAndResumesThrottleThroughManager(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	base := clock.NewFake(time.Date(2026, time.July, 28, 12, 0, 0, 0, time.UTC))
	observed := newProgressiveObservedClock(base)
	fixture := newHLSFixture(t, "hold")
	fixture.ffmpeg = capacityFakeFFmpeg(t)
	logDirectory := filepath.Join(fixture.dir, "logs")
	manager, err := NewTranscodeManager(fixture.ffmpeg, fixture.dir, TranscodeManagerOptions{
		Clock:                  observed,
		OutputPollInterval:     100 * time.Millisecond,
		ReaderRetryInterval:    50 * time.Millisecond,
		ReaderStallTimeout:     30 * time.Second,
		ProgressiveIdleTimeout: 10 * time.Second,
		KillGracePeriod:        5 * time.Second,
		LogDirectory:           logDirectory,
		MaxLogBytes:            1 << 20,
		ThrottleThreshold:      365 * 24 * time.Hour,
		SegmentRetention:       20 * time.Second,
		PKeySupported:          true,
		CacheBytesPerSession:   200,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer closeHLSManager(t, manager)

	t.Setenv("HLS_EMIT_PROGRESS", "1")
	t.Setenv("HLS_PROGRESS_TIME", "00:00:10.000")
	request := fixture.request("cap-budget", 4)
	request.MediaSourceID = "cap-budget"
	request.RunTimeTicks = int64(120 * time.Second / 100)
	if err := manager.StartHLS(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	fixture.waitStarted(t, "cap-budget")
	waitForHLSProgressWithin(t, manager, request.PlaylistPath, fixtureTimeout)

	fixture.writePlaylist(t, request)
	fixture.writeSegment(t, request, 0, strings.Repeat("x", 100))
	fixture.writeSegment(t, request, 1, strings.Repeat("y", 100))
	fixture.writeSegment(t, request, 2, strings.Repeat("z", 100))

	observed.waitForTimer(t, 5*time.Second)
	base.Advance(5 * time.Second)
	fixture.waitP(t, "cap-budget")

	if err := os.Remove(fixture.segmentPath(request, 1)); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(fixture.segmentPath(request, 2)); err != nil {
		t.Fatal(err)
	}

	// The throttler re-arms its 5-second timer after it writes p. Advance
	// only after the second timer exists; an earlier advance moves the
	// clock past nothing, and the throttler then never wakes.
	observed.waitForTimerCount(t, 5*time.Second, 2)
	base.Advance(5 * time.Second)
	fixture.waitU(t, "cap-budget")
}

// capacityFakeFFmpeg builds on the shared hlsFakeFFmpeg script and adds a u
// marker to the hold mode so this test can observe the unpause keystroke.
// hls_lifecycle_test.go is conditionally allowed only for option plumbing,
// so the unpause observation support lives here instead.
func capacityFakeFFmpeg(t *testing.T) string {
	t.Helper()
	base := hlsFakeFFmpeg(t)
	body, err := os.ReadFile(base)
	if err != nil {
		t.Fatal(err)
	}
	body = bytes.Replace(body,
		[]byte(`if [ "$command" = p ]; then : > "$root/p.$label"; fi`),
		[]byte(`if [ "$command" = p ]; then : > "$root/p.$label"; fi
    if [ "$command" = u ]; then : > "$root/u.$label"; fi`),
		1)
	path := filepath.Join(t.TempDir(), "ffmpeg")
	if err := os.WriteFile(path, body, 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func (f hlsFixture) waitU(t *testing.T, label string) {
	t.Helper()
	waitForFile(t, f.marker("u", label))
}

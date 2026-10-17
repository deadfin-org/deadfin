// SPDX-License-Identifier: GPL-2.0-only

package ffmpeg

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/clock"
	"go.uber.org/goleak"
)

const (
	hlsChaosSessions         = 100
	hlsChaosOperationTimeout = 2 * time.Second
	// hlsChaosSeekIndex is far past the 24-second forward gap of every chaos
	// job, so a request for it replaces the process.
	hlsChaosSeekIndex = 1000
)

// TestHLSChaos drives the real manager through a fixed randomized stream of
// sessions. The shell program is only an ffmpeg stand-in: it publishes growing
// HLS artifacts, accepts q, and leaves a same-process-group child behind for
// the manager's process-group reaper to clean up.
func TestHLSChaos(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	fixture := newHLSChaosFixture(t)
	manager := fixture.manager(t)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), hlsChaosOperationTimeout)
		defer cancel()
		if err := manager.Close(ctx); err != nil {
			t.Errorf("manager cleanup: %v", err)
		}
	}()

	const seed uint64 = 0x6a09e667f3bcc909
	random := rand.New(rand.NewPCG(seed, seed^0xbb67ae8584caa73b))
	expectedProcesses := 0

	for session := range hlsChaosSessions {
		label := fmt.Sprintf("chaos-%03d", session)
		request := fixture.request(label, label, session == hlsChaosSessions-1, "")
		fixture.start(t, manager, request, &expectedProcesses)

		fixture.assertCanceledWaiterKeepsJobAlive(t, manager, request)
		fixture.release(t, label)
		fixture.assertServingArtifacts(t, manager, request)

		switch random.IntN(3) {
		case 0:
			// A seek far from the running job must replace the process without
			// overlapping process groups for this output family.
			fixture.restart(t, manager, request, hlsChaosSeekIndex, &expectedProcesses)
			fixture.assertServingArtifacts(t, manager, request)
		case 1:
			// Subtitle changes use a distinct HLS output family but deliberately
			// retain the opaque playback session/device selected by StopPlaySession.
			subtitleLabel := label + "-subtitle"
			subtitle := fixture.request(subtitleLabel, label, false, "1")
			fixture.restart(t, manager, subtitle, 0, &expectedProcesses)
			fixture.release(t, subtitleLabel)
			fixture.assertServingArtifacts(t, manager, subtitle)
		case 2:
			// Couple a seek with a subtitle-family switch to exercise the paths
			// concurrently owned by one exact play-session/device pair.
			fixture.restart(t, manager, request, hlsChaosSeekIndex, &expectedProcesses)
			fixture.assertServingArtifacts(t, manager, request)
			subtitleLabel := label + "-subtitle"
			subtitle := fixture.request(subtitleLabel, label, false, "2")
			fixture.restart(t, manager, subtitle, 0, &expectedProcesses)
			fixture.release(t, subtitleLabel)
			fixture.assertServingArtifacts(t, manager, subtitle)
		}

		if session != hlsChaosSessions-1 {
			fixture.stopSession(t, manager, request)
		}
	}

	// The last helper deliberately ignores q. A canceled shutdown wait must not
	// strand the manager: a later Close call must finish its kill/reap/cleanup.
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := manager.Close(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("first Close with canceled caller context = %v, want context.Canceled", err)
	}
	fixture.close(t, manager)

	fixture.assertNoArtifacts(t)
	fixture.assertIndexesEmpty(t, manager)
	fixture.assertAllProcessGroupsGone(t)
}

type hlsChaosFixture struct {
	root       string
	directory  string
	ffmpegPath string
}

func newHLSChaosFixture(t *testing.T) hlsChaosFixture {
	t.Helper()
	root := t.TempDir()
	fixture := hlsChaosFixture{
		root:      root,
		directory: filepath.Join(root, "transcodes"),
	}
	fixture.ffmpegPath = hlsChaosFakeFFmpeg(t)
	return fixture
}

func (f hlsChaosFixture) manager(t *testing.T) *TranscodeManager {
	t.Helper()
	manager, err := NewTranscodeManager(f.ffmpegPath, f.directory, TranscodeManagerOptions{
		Clock:                  clock.NewReal(),
		OutputPollInterval:     5 * time.Millisecond,
		ReaderRetryInterval:    5 * time.Millisecond,
		ReaderStallTimeout:     hlsChaosOperationTimeout,
		ProgressiveIdleTimeout: time.Hour,
		KillGracePeriod:        50 * time.Millisecond,
		LogDirectory:           filepath.Join(f.root, "logs"),
		ThrottleThreshold:      time.Hour,
		SegmentRetention:       time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func (f hlsChaosFixture) request(label, session string, ignoreQ bool, subtitle string) HLSRequest {
	playlist := filepath.Join(f.directory, label+".m3u8")
	pattern := filepath.Join(f.directory, label+"%d.ts")
	args := []string{
		"--chaos-root=" + f.root,
		"--chaos-label=" + label,
	}
	if ignoreQ {
		args = append(args, "--chaos-ignore-q")
	}
	if subtitle != "" {
		args = append(args, "--chaos-subtitle-stream="+subtitle)
	}
	args = append(args,
		"-hls_segment_filename", pattern,
		playlist,
	)
	return HLSRequest{
		PlaylistPath:         playlist,
		Live:                 true,
		MinSegments:          2,
		SegmentPattern:       pattern,
		Args:                 args,
		PlaySessionID:        "play-session-" + session,
		DeviceID:             "device-" + session,
		MediaSourceID:        "source-" + session,
		SegmentLengthSeconds: 4,
		RunTimeTicks:         600 * 10_000_000,
	}
}

func (f hlsChaosFixture) start(t *testing.T, manager *TranscodeManager, request HLSRequest, expectedProcesses *int) {
	t.Helper()
	f.call(t, "StartHLS", func(ctx context.Context) error { return manager.StartHLS(ctx, request) })
	*expectedProcesses = *expectedProcesses + 1
	f.waitForProcesses(t, *expectedProcesses)
}

func (f hlsChaosFixture) restart(t *testing.T, manager *TranscodeManager, request HLSRequest, index int, expectedProcesses *int) {
	t.Helper()
	f.call(t, fmt.Sprintf("RestartHLSAt(%d)", index), func(ctx context.Context) error {
		return manager.RestartHLSAt(ctx, request, index)
	})
	*expectedProcesses = *expectedProcesses + 1
	f.waitForProcesses(t, *expectedProcesses)
}

func (f hlsChaosFixture) call(t *testing.T, operation string, call func(context.Context) error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), hlsChaosOperationTimeout)
	defer cancel()
	if err := call(ctx); err != nil {
		t.Fatalf("%s: %v", operation, err)
	}
}

func (f hlsChaosFixture) release(t *testing.T, label string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.root, "release."+label), nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

func (f hlsChaosFixture) assertCanceledWaiterKeepsJobAlive(t *testing.T, manager *TranscodeManager, request HLSRequest) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	results := make(chan hlsWaitResult, 1)
	go func() {
		file, err := manager.WaitSegment(ctx, request.PlaylistPath, 0)
		results <- hlsWaitResult{file: file, err: err}
	}()
	cancel()

	result := f.receiveWait(t, results, "canceled segment waiter")
	if result.file != nil {
		_ = result.file.Close()
	}
	if !errors.Is(result.err, context.Canceled) {
		t.Fatalf("WaitSegment canceled before segment output = %v, want context.Canceled", result.err)
	}
	if !manager.PingPlaySession(request.PlaySessionID) {
		t.Fatalf("canceled segment waiter removed active play session %q", request.PlaySessionID)
	}
}

func (f hlsChaosFixture) assertServingArtifacts(t *testing.T, manager *TranscodeManager, request HLSRequest) {
	t.Helper()
	segmentResults := make(chan hlsWaitResult, 1)
	playlistResults := make(chan hlsPlaylistResult, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), hlsChaosOperationTimeout)
		defer cancel()
		file, err := manager.WaitSegment(ctx, request.PlaylistPath, 0)
		segmentResults <- hlsWaitResult{file: file, err: err}
	}()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), hlsChaosOperationTimeout)
		defer cancel()
		playlist, err := manager.WaitPlaylist(ctx, request.PlaylistPath, 2)
		playlistResults <- hlsPlaylistResult{playlist: playlist, err: err}
	}()

	segment := f.receiveWait(t, segmentResults, "segment waiter")
	if segment.err != nil {
		t.Fatalf("WaitSegment: %v", segment.err)
	}
	if segment.file == nil {
		t.Fatal("WaitSegment returned nil file without an error")
	}
	data, err := io.ReadAll(segment.file)
	closeErr := segment.file.Close()
	if err != nil {
		t.Fatalf("read segment: %v", err)
	}
	if closeErr != nil {
		t.Fatalf("close segment: %v", closeErr)
	}
	wantSegment := strings.TrimSuffix(filepath.Base(request.PlaylistPath), ".m3u8") + ":0\n"
	if got := string(data); got != wantSegment {
		t.Fatalf("segment bytes = %q, want %q", got, wantSegment)
	}

	playlist := f.receivePlaylist(t, playlistResults)
	if playlist.err != nil {
		t.Fatalf("WaitPlaylist: %v", playlist.err)
	}
	if completePlaylistSegments(playlist.playlist) < request.MinSegments {
		t.Fatalf("WaitPlaylist returned %d complete segments, want at least %d", completePlaylistSegments(playlist.playlist), request.MinSegments)
	}
}

func (f hlsChaosFixture) stopSession(t *testing.T, manager *TranscodeManager, request HLSRequest) {
	t.Helper()
	f.call(t, "StopPlaySession", func(ctx context.Context) error {
		return manager.StopPlaySession(ctx, request.PlaySessionID, request.DeviceID)
	})
}

func (f hlsChaosFixture) close(t *testing.T, manager *TranscodeManager) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), hlsChaosOperationTimeout)
	defer cancel()
	if err := manager.Close(ctx); err != nil {
		t.Fatalf("retry Close: %v", err)
	}
}

func (f hlsChaosFixture) waitForProcesses(t *testing.T, want int) {
	t.Helper()
	waitForProgressiveStartRecords(t, filepath.Join(f.root, "leaders"), want)
	waitForProgressiveStartRecords(t, filepath.Join(f.root, "children"), want)
}

func (f hlsChaosFixture) receiveWait(t *testing.T, results <-chan hlsWaitResult, description string) hlsWaitResult {
	t.Helper()
	timer := time.NewTimer(hlsChaosOperationTimeout)
	defer timer.Stop()
	select {
	case result := <-results:
		return result
	case <-timer.C:
		t.Fatalf("timed out waiting for %s", description)
		return hlsWaitResult{}
	}
}

func (f hlsChaosFixture) receivePlaylist(t *testing.T, results <-chan hlsPlaylistResult) hlsPlaylistResult {
	t.Helper()
	timer := time.NewTimer(hlsChaosOperationTimeout)
	defer timer.Stop()
	select {
	case result := <-results:
		return result
	case <-timer.C:
		t.Fatal("timed out waiting for playlist waiter")
		return hlsPlaylistResult{}
	}
}

func (f hlsChaosFixture) assertNoArtifacts(t *testing.T) {
	t.Helper()
	entries, err := os.ReadDir(f.directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("descriptor-owned transcode root retains artifacts: %s", strings.Join(names, ", "))
	}
}

func (f hlsChaosFixture) assertIndexesEmpty(t *testing.T, manager *TranscodeManager) {
	t.Helper()
	manager.mu.Lock()
	jobs, sessions, locks := len(manager.jobs), len(manager.hlsSessions), len(manager.hlsLocks)
	lockUsers, workers := len(manager.hlsLockUsers), manager.workerCount
	manager.mu.Unlock()
	if jobs != 0 || sessions != 0 || locks != 0 || lockUsers != 0 || workers != 0 {
		t.Fatalf("manager retained lifecycle indexes: jobs=%d sessions=%d locks=%d lockUsers=%d workers=%d", jobs, sessions, locks, lockUsers, workers)
	}
}

func (f hlsChaosFixture) assertAllProcessGroupsGone(t *testing.T) {
	t.Helper()
	leaders := readLines(t, filepath.Join(f.root, "leaders"))
	for _, path := range []string{filepath.Join(f.root, "leaders"), filepath.Join(f.root, "children")} {
		for _, line := range readLines(t, path) {
			pid, err := strconv.Atoi(line)
			if err != nil || pid <= 0 {
				t.Fatalf("invalid helper PID record %q in %s", line, path)
			}
			waitForProcessExit(t, pid)
		}
	}
	for _, line := range leaders {
		pid, err := strconv.Atoi(line)
		if err != nil {
			t.Fatal(err)
		}
		f.waitForProcessGroupExit(t, pid)
	}
}

func (f hlsChaosFixture) waitForProcessGroupExit(t *testing.T, leader int) {
	t.Helper()
	timer := time.NewTimer(hlsChaosOperationTimeout)
	defer timer.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if err := syscall.Kill(-leader, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		select {
		case <-timer.C:
			_ = syscall.Kill(-leader, syscall.SIGKILL)
			t.Fatalf("helper process group %d survived manager cleanup", leader)
		case <-ticker.C:
		}
	}
}

func hlsChaosFakeFFmpeg(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ffmpeg")
	body := `#!/bin/sh
set -eu
root=
label=
pattern=
playlist=
ignore_q=0
while [ "$#" -gt 0 ]; do
  case "$1" in
    --chaos-root=*) root=${1#--chaos-root=} ;;
    --chaos-label=*) label=${1#--chaos-label=} ;;
    --chaos-ignore-q) ignore_q=1 ;;
    -hls_segment_filename) shift; pattern=$1 ;;
    *.m3u8) playlist=$1 ;;
  esac
  shift
done
[ -n "$root" ]
[ -n "$label" ]
[ -n "$pattern" ]
[ -n "$playlist" ]
printf '%s\n' "$$" >> "$root/leaders"
(
  trap '' TERM
  while :; do sleep 1; done
) &
child=$!
printf '%s\n' "$child" >> "$root/children"
: > "$root/started.$label"
while [ ! -e "$root/release.$label" ]; do sleep 0.005; done
trap 'exit 0' USR1
(
  while IFS= read -r command; do
    if [ "$command" = q ]; then
      : > "$root/q.$label"
      if [ "$ignore_q" -eq 0 ]; then kill -USR1 "$$"; fi
      exit 0
    fi
  done
) &
index=0
printf '#EXTM3U\n' > "$playlist"
while :; do
  segment=$(printf "$pattern" "$index")
  printf '%s:%s\n' "$label" "$index" > "$segment"
  printf '#EXTINF:4,\n%s\n' "$(basename "$segment")" >> "$playlist"
  index=$((index + 1))
  sleep 0.005
done
`
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// SPDX-License-Identifier: GPL-2.0-only

package ffmpeg

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/clock"
	"go.uber.org/goleak"
)

// These tests exercise HLS lifecycle behavior through the same manager that owns
// progressive ffmpeg processes. The shell program is only an observable child
// process; the manager remains responsible for deduplication, readiness, idle
// shutdown, session selection, and cleanup.
func TestStartHLSDeduplicatesIdenticalRequestAndRejectsConflictingArgv(t *testing.T) {
	fixture := newHLSFixture(t, "hold")
	manager := fixture.manager(t, clock.NewReal())
	defer closeHLSManager(t, manager)
	request := fixture.request("shared", 4)

	if err := manager.StartHLS(context.Background(), request); err != nil {
		t.Fatalf("first StartHLS: %v", err)
	}
	if err := manager.StartHLS(context.Background(), request); err != nil {
		t.Fatalf("identical StartHLS: %v", err)
	}
	fixture.waitStarted(t, "shared")

	conflict := request
	conflict.Args = append([]string(nil), request.Args...)
	conflict.Args[0] = "--fixture-label=conflict"
	if err := manager.StartHLS(context.Background(), conflict); err == nil {
		t.Fatal("StartHLS accepted conflicting argv for the shared playlist")
	}
	if got := hlsStartCount(t, fixture, "shared"); got != 1 {
		t.Fatalf("shared playlist starts = %d, want one process after dedupe/conflict", got)
	}
}

func TestStartHLSDoesNotJoinChangedRenditionArgsIntoExistingFamily(t *testing.T) {
	fixture := newHLSFixture(t, "hold")
	manager := fixture.manager(t, clock.NewReal())
	defer closeHLSManager(t, manager)
	request := fixture.request("profile-family", 4)
	request.Args = []string{"--fixture-label=profile-family", "-profile:v:0", "high", "-level:v:0", "4.1", "-maxrate", "1000000"}

	if err := manager.StartHLS(t.Context(), request); err != nil {
		t.Fatalf("first StartHLS: %v", err)
	}
	if err := manager.StartHLS(t.Context(), request); err != nil {
		t.Fatalf("identical StartHLS: %v", err)
	}
	fixture.waitStarted(t, "profile-family")

	changed := request
	changed.Args = append([]string(nil), request.Args...)
	changed.Args[2], changed.Args[4], changed.Args[6] = "main", "4.0", "800000"
	if err := manager.StartHLS(t.Context(), changed); err == nil || !strings.Contains(err.Error(), "conflicting ffmpeg argv") {
		t.Fatalf("changed rendition StartHLS error = %v, want conflicting argv rather than serving the existing family", err)
	}
	if got := hlsStartCount(t, fixture, "profile-family"); got != 1 {
		t.Fatalf("profile family starts = %d, want exact-request dedupe only", got)
	}
}

func TestStartHLSConcurrentlyDeduplicatesIdenticalRenditionRequest(t *testing.T) {
	fixture := newHLSFixture(t, "hold")
	manager := fixture.manager(t, clock.NewReal())
	defer closeHLSManager(t, manager)
	request := fixture.request("concurrent-rendition", 4)
	request.Args = []string{"--fixture-label=concurrent-rendition", "-profile:v:0", "main", "-level:v:0", "4.0", "-maxrate", "800000"}

	const callers = 16
	start := make(chan struct{})
	errs := make(chan error, callers)
	for range callers {
		go func() {
			<-start
			errs <- manager.StartHLS(t.Context(), request)
		}()
	}
	close(start)
	for range callers {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent identical StartHLS: %v", err)
		}
	}
	fixture.waitStarted(t, "concurrent-rendition")
	if got := hlsStartCount(t, fixture, "concurrent-rendition"); got != 1 {
		t.Fatalf("concurrent identical rendition starts = %d, want one", got)
	}
}

func TestRestartHLSAtChangedArgsReplacesFamilyBeforeCachedOrForwardReuse(t *testing.T) {
	for _, test := range []struct {
		name          string
		index         int
		writeExisting func(hlsFixture, HLSRequest)
	}{
		{
			name:  "cached nearby segment",
			index: 0,
			writeExisting: func(fixture hlsFixture, request HLSRequest) {
				fixture.writeSegment(t, request, 0, "A cached segment")
				fixture.writeSegment(t, request, 1, "A cached successor")
			},
		},
		{
			name:  "forward index within reuse window",
			index: 11,
			writeExisting: func(fixture hlsFixture, request HLSRequest) {
				fixture.writeSegment(t, request, 10, "A current segment")
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newHLSFixture(t, "hold")
			manager := fixture.manager(t, clock.NewReal())
			defer closeHLSManager(t, manager)
			requestA := fixture.request("changed-argv-A", 4)
			requestA.Args = []string{"--fixture-label=changed-argv-A", "-profile:v:0", "high", "-level:v:0", "4.1"}
			if err := manager.StartHLS(t.Context(), requestA); err != nil {
				t.Fatalf("StartHLS A: %v", err)
			}
			fixture.waitStarted(t, "changed-argv-A")
			pidA := fixture.waitPID(t, "changed-argv-A")
			fixture.writePlaylist(t, requestA)
			test.writeExisting(fixture, requestA)

			requestB := requestA
			requestB.Args = []string{"--fixture-label=changed-argv-B", "-profile:v:0", "main", "-level:v:0", "4.0"}
			registered := make(chan struct{})
			manager.beforeHLSRestartRegister = func() { close(registered) }
			t.Cleanup(func() { manager.beforeHLSRestartRegister = nil })
			done := make(chan error, 1)
			go func() { done <- manager.RestartHLSAt(t.Context(), requestB, test.index) }()

			select {
			case <-registered:
			case <-time.After(time.Second):
				t.Fatal("changed ffmpeg argv returned through a cached/forward reuse path instead of registering a replacement")
			}
			fixture.waitStarted(t, "changed-argv-B")
			fixture.writeSegment(t, requestB, test.index, "B replacement segment")
			if err := receiveProgressiveError(t, done); err != nil {
				t.Fatalf("RestartHLSAt B: %v", err)
			}
			fixture.writeSegment(t, requestB, test.index+1, "B replacement successor")
			waitForProcessExit(t, pidA)
			if got := hlsStartCount(t, fixture, "changed-argv-A"); got != 1 {
				t.Fatalf("A starts = %d, want one retired process", got)
			}
			if got := hlsStartCount(t, fixture, "changed-argv-B"); got != 1 {
				t.Fatalf("B starts = %d, want one replacement process", got)
			}
			file, err := manager.WaitSegment(t.Context(), requestB.PlaylistPath, test.index)
			if err != nil {
				t.Fatalf("WaitSegment B: %v", err)
			}
			if got := readHLSSegment(t, file); got != "B replacement segment" {
				_ = file.Close()
				t.Fatalf("replacement segment bytes = %q, want B bytes rather than A cache", got)
			}
			closeHLSSegment(t, file)
		})
	}
}

// hlsSegmentRequest returns the request that the segment service makes for
// one segment of a rendition: its own seek and start number, and the shared
// rendition identity.
func hlsSegmentRequest(base HLSRequest, label string, index int) HLSRequest {
	request := base
	request.IdentityArgs = []string{"--fixture-label=" + label, "-codec:v:0", "copy"}
	request.Args = []string{"--fixture-label=" + label}
	if index > 0 {
		request.Args = append(request.Args, "-ss", fmt.Sprintf("00:00:%02d.500", index*6))
	}
	request.Args = append(request.Args, "-codec:v:0", "copy", "-start_number", strconv.Itoa(index))
	return request
}

// Upstream keys an HLS job on its playlist path and restarts only for a
// backward request, a forward gap, or an exited job
// (DynamicHlsController.GetDynamicSegment). A sequential segment request has a
// different seek and start number in its argv; it must join the running job.
// A relaunch for every segment made a video-copy remux serve wrong, overlapping
// media to AVPlayer.
func TestRestartHLSAtJoinsRunningFamilyForSequentialSegmentArgv(t *testing.T) {
	for _, tc := range []struct {
		name    string
		index   int
		current int
	}{
		{name: "cached segment behind the encoder", index: 1, current: 2},
		{name: "segment being encoded", index: 3, current: 2},
		{name: "forward request within the reuse window", index: 6, current: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const label = "sequential"
			fixture := newHLSFixture(t, "hold")
			manager := fixture.manager(t, clock.NewReal())
			defer closeHLSManager(t, manager)
			base := fixture.request(label, 6)
			first := hlsSegmentRequest(base, label, 0)
			if err := manager.RestartHLSAt(t.Context(), first, 0); err != nil {
				t.Fatalf("RestartHLSAt(0): %v", err)
			}
			fixture.waitStarted(t, label)
			fixture.writePlaylist(t, first)
			for index := 0; index <= tc.current; index++ {
				fixture.writeSegment(t, first, index, "segment "+strconv.Itoa(index))
			}

			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			if err := manager.RestartHLSAt(ctx, hlsSegmentRequest(base, label, tc.index), tc.index); err != nil {
				t.Fatalf("RestartHLSAt(%d): %v", tc.index, err)
			}
			if got := hlsStartCount(t, fixture, label); got != 1 {
				t.Fatalf("ffmpeg starts after sequential segment %d = %d, want the running job reused", tc.index, got)
			}
		})
	}
}

// A keyframe-cut remux counts segment indexes in source GOPs, so the caller
// widens the forward reuse window past 24/SegmentLengthSeconds (deadfin#629).
func TestRestartHLSAtHonorsMaxForwardGap(t *testing.T) {
	for _, tc := range []struct {
		name       string
		gap        int
		wantStarts int
	}{
		{name: "default window restarts", gap: 0, wantStarts: 2},
		{name: "wider window joins", gap: 20, wantStarts: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const label = "gap"
			fixture := newHLSFixture(t, "hold")
			manager := fixture.manager(t, clock.NewReal())
			defer closeHLSManager(t, manager)
			base := fixture.request(label, 6)
			base.MaxForwardGap = tc.gap
			first := hlsSegmentRequest(base, label, 0)
			if err := manager.RestartHLSAt(t.Context(), first, 0); err != nil {
				t.Fatalf("RestartHLSAt(0): %v", err)
			}
			fixture.waitStarted(t, label)
			fixture.writePlaylist(t, first)
			fixture.writeSegment(t, first, 0, "segment 0")

			ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
			defer cancel()
			_ = manager.RestartHLSAt(ctx, hlsSegmentRequest(base, label, 10), 10)
			waitForHLSStarts(t, fixture, label, tc.wantStarts)
			if got := hlsStartCount(t, fixture, label); got != tc.wantStarts {
				t.Fatalf("ffmpeg starts after a forward request of 10 = %d, want %d", got, tc.wantStarts)
			}
		})
	}
}

func TestRestartHLSAtReplacesFamilyWhenIdentityChanges(t *testing.T) {
	const label = "identity"
	fixture := newHLSFixture(t, "hold")
	manager := fixture.manager(t, clock.NewReal())
	defer closeHLSManager(t, manager)
	base := fixture.request(label, 6)
	first := hlsSegmentRequest(base, label, 0)
	if err := manager.RestartHLSAt(t.Context(), first, 0); err != nil {
		t.Fatalf("RestartHLSAt(0): %v", err)
	}
	fixture.waitStarted(t, label)
	fixture.writePlaylist(t, first)
	fixture.writeSegment(t, first, 0, "A")
	fixture.writeSegment(t, first, 1, "A successor")

	changed := hlsSegmentRequest(base, label, 1)
	changed.IdentityArgs = []string{"--fixture-label=" + label, "-codec:v:0", "libx264"}
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	_ = manager.RestartHLSAt(ctx, changed, 1)
	if got := len(waitForHLSStarts(t, fixture, label, 2)); got != 2 {
		t.Fatalf("ffmpeg starts after identity change = %d, want a replacement job", got)
	}
}

func TestStopPlaySessionPreventsConcurrentStartHLSAdmissionForSamePair(t *testing.T) {
	fixture := newHLSFixture(t, "hold")
	manager := fixture.manager(t, clock.NewReal())
	defer closeHLSManager(t, manager)
	request := fixture.request("start-admission", 4)
	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	manager.beforeHLSStartRegister = func() {
		close(entered)
		<-release
	}
	t.Cleanup(func() {
		manager.beforeHLSStartRegister = nil
		releaseOnce.Do(func() { close(release) })
	})
	started := make(chan error, 1)
	go func() { started <- manager.StartHLS(context.Background(), request) }()
	waitForProgressiveSignal(t, entered, "StartHLS admission barrier")
	if err := manager.StopPlaySession(context.Background(), request.PlaySessionID, request.DeviceID); err != nil {
		t.Fatalf("StopPlaySession during StartHLS admission: %v", err)
	}
	releaseOnce.Do(func() { close(release) })
	if err := receiveProgressiveError(t, started); err == nil {
		t.Fatal("StartHLS succeeded after StopPlaySession invalidated its admission epoch")
	}
	if got := hlsStartCount(t, fixture, "start-admission"); got != 0 {
		t.Fatalf("concurrent StartHLS launched %d processes after StopPlaySession, want none", got)
	}
	for _, path := range []string{request.PlaylistPath, fixture.segmentPath(request, 0)} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("concurrent StartHLS left artifact %q: %v", path, err)
		}
	}
	manager.mu.Lock()
	jobs, sessions, locks := len(manager.jobs), len(manager.hlsSessions), len(manager.hlsLocks)
	manager.mu.Unlock()
	if jobs != 0 || sessions != 0 || locks != 0 {
		t.Fatalf("concurrent StartHLS left admission state: jobs=%d sessions=%d locks=%d", jobs, sessions, locks)
	}
	manager.beforeHLSStartRegister = nil
	if err := manager.StartHLS(context.Background(), request); err != nil {
		t.Fatalf("StartHLS after completed StopPlaySession: %v", err)
	}
	fixture.waitStarted(t, "start-admission")
	if got := hlsStartCount(t, fixture, "start-admission"); got != 1 {
		t.Fatalf("StartHLS after completed StopPlaySession starts = %d, want one", got)
	}
}

func TestWaitSegmentReturnsNOnlyAfterNPlusOneAtInjectedPollInterval(t *testing.T) {
	base := clock.NewFake(time.Date(2026, time.July, 28, 12, 0, 0, 0, time.UTC))
	observed := newProgressiveObservedClock(base)
	fixture := newHLSFixture(t, "hold")
	manager := fixture.manager(t, observed)
	defer closeHLSManager(t, manager)
	request := fixture.request("readiness", 4)
	if err := manager.StartHLS(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	fixture.waitStarted(t, "readiness")
	fixture.writePlaylist(t, request)
	fixture.writeSegment(t, request, 0, "segment zero")

	results := make(chan hlsWaitResult, 1)
	go func() {
		file, err := manager.WaitSegment(context.Background(), request.PlaylistPath, 0)
		results <- hlsWaitResult{file: file, err: err}
	}()
	observed.waitForTimer(t, 100*time.Millisecond)
	assertHLSPending(t, results)

	fixture.writeSegment(t, request, 1, "segment one")
	base.Advance(100 * time.Millisecond)
	file := receiveHLSFile(t, results)
	defer closeHLSSegment(t, file)
	if got := readHLSSegment(t, file); got != "segment zero" {
		t.Fatalf("WaitSegment(0) bytes = %q, want segment zero after segment 1 exists", got)
	}
}

func TestWaitSegmentWaitsForFMP4InitializationSuccessor(t *testing.T) {
	base := clock.NewFake(time.Date(2026, time.July, 28, 12, 0, 0, 0, time.UTC))
	observed := newProgressiveObservedClock(base)
	fixture := newHLSFixture(t, "hold")
	manager := fixture.manager(t, observed)
	defer closeHLSManager(t, manager)
	request := fixture.requestWithExtension("init-successor", 4, ".mp4")
	if err := manager.StartHLS(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	fixture.waitStarted(t, "init-successor")
	fixture.writePlaylist(t, request)

	// A complete fMP4 initialization segment is a 16-byte ftyp box plus an
	// 8-byte moov box. The five-byte prefix cannot contain either box.
	complete := makeFMP4Initialization(t, 16, "ftyp", []byte("isom0001"), 8, "moov")
	fixture.writeSegment(t, request, -1, string(complete[:5]))

	results := make(chan hlsWaitResult, 1)
	go func() {
		file, err := manager.WaitSegment(context.Background(), request.PlaylistPath, -1)
		results <- hlsWaitResult{file: file, err: err}
	}()
	observed.waitForTimer(t, 100*time.Millisecond)
	assertHLSPending(t, results)

	fixture.writeSegment(t, request, -1, string(complete))
	fixture.writeSegment(t, request, 0, "segment zero")
	base.Advance(100 * time.Millisecond)
	file := receiveHLSFile(t, results)
	defer closeHLSSegment(t, file)
	if got := readHLSSegment(t, file); got != string(complete) {
		t.Fatalf("WaitSegment(-1) bytes = %q, want complete fMP4 initialization after segment 0 exists", got)
	}
}

func TestWaitPlaylistReturnsOnlyCompleteEntriesAfterInjectedPolling(t *testing.T) {
	base := clock.NewFake(time.Date(2026, time.July, 28, 12, 0, 0, 0, time.UTC))
	observed := newProgressiveObservedClock(base)
	fixture := newHLSFixture(t, "hold")
	manager := fixture.manager(t, observed)
	defer closeHLSManager(t, manager)
	request := fixture.request("playlist", 4)
	request.Live = true
	request.MinSegments = 2
	if err := manager.StartHLS(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	fixture.waitStarted(t, "playlist")
	fixture.writePlaylistText(t, request, "#EXTM3U\n#EXTINF:4,\nplaylist0.ts\n")

	results := make(chan hlsPlaylistResult, 1)
	go func() {
		playlist, err := manager.WaitPlaylist(context.Background(), request.PlaylistPath, 2)
		results <- hlsPlaylistResult{playlist: playlist, err: err}
	}()
	observed.waitForTimer(t, 100*time.Millisecond)
	assertHLSPlaylistPending(t, results)

	want := "#EXTM3U\n#EXTINF:4,\nplaylist0.ts\n#EXTINF:4,\nplaylist1.ts\n"
	fixture.writePlaylistText(t, request, want)
	base.Advance(100 * time.Millisecond)
	result := receiveHLSPlaylist(t, results)
	if result.err != nil {
		t.Fatalf("WaitPlaylist: %v", result.err)
	}
	if result.playlist != want {
		t.Fatalf("WaitPlaylist text = %q, want %q", result.playlist, want)
	}
}

func TestLiveHLSAllowsZeroMinimumSegmentsAndRejectsNegativeMinimum(t *testing.T) {
	fixture := newHLSFixture(t, "hold")
	manager := fixture.manager(t, clock.NewReal())
	defer closeHLSManager(t, manager)
	request := fixture.request("live-zero-minimum", 4)
	request.Live = true
	request.MinSegments = 0
	if err := manager.StartHLS(context.Background(), request); err != nil {
		t.Fatalf("StartHLS live zero minimum: %v", err)
	}
	fixture.waitStarted(t, "live-zero-minimum")
	want := "#EXTM3U\n#EXT-X-VERSION:3\n"
	fixture.writePlaylistText(t, request, want)
	playlist, err := manager.WaitPlaylist(context.Background(), request.PlaylistPath, 0)
	if err != nil {
		t.Fatalf("WaitPlaylist live zero minimum: %v", err)
	}
	if playlist != want {
		t.Fatalf("zero-minimum live playlist = %q, want %q", playlist, want)
	}

	negative := fixture.request("live-negative-minimum", 4)
	negative.Live = true
	negative.MinSegments = -1
	if err := manager.StartHLS(context.Background(), negative); err == nil {
		t.Fatal("StartHLS accepted a negative live MinSegments value")
	}
	if _, err := os.Stat(fixture.marker("started", "live-negative-minimum")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("negative live MinSegments started ffmpeg: %v", err)
	}
}

func TestWaitPlaylistRejectsSymlinkedPathOutsideOwnedFamily(t *testing.T) {
	fixture := newHLSFixture(t, "hold")
	manager := fixture.manager(t, clock.NewReal())
	defer closeHLSManager(t, manager)
	request := fixture.request("playlist-link", 4)
	if err := manager.StartHLS(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	fixture.waitStarted(t, "playlist-link")
	outside := filepath.Join(t.TempDir(), "outside.m3u8")
	if err := os.WriteFile(outside, []byte("#EXTM3U\n#EXTINF:4,\nsecret.ts\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, request.PlaylistPath); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.WaitPlaylist(context.Background(), request.PlaylistPath, 1); err == nil {
		t.Fatal("WaitPlaylist followed a playlist symlink outside the owned HLS family")
	}
}

func TestWaitSegmentFinalSegmentBecomesReadyOnlyAfterProcessExit(t *testing.T) {
	base := clock.NewFake(time.Date(2026, time.July, 28, 12, 0, 0, 0, time.UTC))
	observed := newProgressiveObservedClock(base)
	fixture := newHLSFixture(t, "exit-on-go")
	manager := fixture.manager(t, observed)
	defer closeHLSManager(t, manager)
	request := fixture.request("final", 4)
	if err := manager.StartHLS(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	fixture.waitStarted(t, "final")
	pid := fixture.waitPID(t, "final")
	fixture.writePlaylist(t, request)
	fixture.writeSegment(t, request, 0, "final segment")

	results := make(chan hlsWaitResult, 1)
	go func() {
		file, err := manager.WaitSegment(context.Background(), request.PlaylistPath, 0)
		results <- hlsWaitResult{file: file, err: err}
	}()
	observed.waitForTimer(t, 100*time.Millisecond)
	assertHLSPending(t, results)

	fixture.release(t, "final")
	waitForProcessExit(t, pid)
	base.Advance(100 * time.Millisecond)
	file := receiveHLSFile(t, results)
	defer closeHLSSegment(t, file)
	if got := readHLSSegment(t, file); got != "final segment" {
		t.Fatalf("final WaitSegment bytes = %q, want final segment after ffmpeg exit", got)
	}
}

func TestWaitSegmentDoesNotLeakDescriptorsAcrossPlaylistFamily(t *testing.T) {
	fixture := newHLSFixture(t, "hold")
	manager := fixture.manager(t, clock.NewReal())
	defer closeHLSManager(t, manager)
	request := fixture.request("family", 4)
	if err := manager.StartHLS(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	fixture.waitStarted(t, "family")
	fixture.writePlaylist(t, request)
	for index := range 25 {
		fixture.writeSegment(t, request, index, fmt.Sprintf("segment %d", index))
	}
	before := progressiveOpenFileCount(t)
	for index := range 24 {
		file, err := manager.WaitSegment(context.Background(), request.PlaylistPath, index)
		if err != nil {
			t.Fatalf("WaitSegment(%d): %v", index, err)
		}
		if got, want := readHLSSegment(t, file), fmt.Sprintf("segment %d", index); got != want {
			_ = file.Close()
			t.Fatalf("WaitSegment(%d) bytes = %q, want %q", index, got, want)
		}
		closeHLSSegment(t, file)
	}
	after := progressiveOpenFileCount(t)
	if after > before+1 {
		t.Fatalf("repeated HLS segment opens leaked descriptors: before=%d after=%d", before, after)
	}
}

func TestWaitSegmentLeasePreventsHLSIdleStopUntilCancellation(t *testing.T) {
	base := clock.NewFake(time.Date(2026, time.July, 28, 12, 0, 0, 0, time.UTC))
	observed := newProgressiveObservedClock(base)
	fixture := newHLSFixture(t, "hold")
	manager := fixture.manager(t, observed)
	defer closeHLSManager(t, manager)
	request := fixture.request("segment-lease", 4)
	if err := manager.StartHLS(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	fixture.waitStarted(t, "segment-lease")
	pid := fixture.waitPID(t, "segment-lease")
	observed.waitForTimer(t, 60*time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	results := make(chan hlsWaitResult, 1)
	go func() {
		file, err := manager.WaitSegment(ctx, request.PlaylistPath, 0)
		results <- hlsWaitResult{file: file, err: err}
	}()
	observed.waitForTimer(t, 100*time.Millisecond)

	base.Advance(60 * time.Second)
	assertHLSPending(t, results)
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("active WaitSegment lost its HLS job to idle shutdown: %v", err)
	}

	cancel()
	result := receiveHLSWait(t, results)
	if result.file != nil {
		_ = result.file.Close()
	}
	if !errors.Is(result.err, context.Canceled) {
		t.Fatalf("WaitSegment cancellation = %v, want context.Canceled", result.err)
	}
	observed.waitForTimerCount(t, 60*time.Second, 2)
	base.Advance(60 * time.Second)
	fixture.waitQ(t, "segment-lease")
	waitForProcessExit(t, pid)
}

func TestWaitPlaylistLeasePreventsHLSIdleStopUntilCancellation(t *testing.T) {
	base := clock.NewFake(time.Date(2026, time.July, 28, 12, 0, 0, 0, time.UTC))
	observed := newProgressiveObservedClock(base)
	fixture := newHLSFixture(t, "hold")
	manager := fixture.manager(t, observed)
	defer closeHLSManager(t, manager)
	request := fixture.request("playlist-lease", 4)
	request.Live = true
	if err := manager.StartHLS(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	fixture.waitStarted(t, "playlist-lease")
	pid := fixture.waitPID(t, "playlist-lease")
	fixture.writePlaylistText(t, request, "#EXTM3U\n#EXTINF:4,\nplaylist-lease0.ts\n")
	observed.waitForTimer(t, 60*time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	results := make(chan hlsPlaylistResult, 1)
	go func() {
		playlist, err := manager.WaitPlaylist(ctx, request.PlaylistPath, 2)
		results <- hlsPlaylistResult{playlist: playlist, err: err}
	}()
	observed.waitForTimer(t, 100*time.Millisecond)

	base.Advance(60 * time.Second)
	assertHLSPlaylistPending(t, results)
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("active WaitPlaylist lost its HLS job to idle shutdown: %v", err)
	}

	cancel()
	result := receiveHLSPlaylist(t, results)
	if !errors.Is(result.err, context.Canceled) {
		t.Fatalf("WaitPlaylist cancellation = %v, want context.Canceled", result.err)
	}
	observed.waitForTimerCount(t, 60*time.Second, 2)
	base.Advance(60 * time.Second)
	fixture.waitQ(t, "playlist-lease")
	waitForProcessExit(t, pid)
}

func TestConcurrentHLSWaiterLeasesBalanceBeforeArmingIdleStop(t *testing.T) {
	base := clock.NewFake(time.Date(2026, time.July, 28, 12, 0, 0, 0, time.UTC))
	observed := newProgressiveObservedClock(base)
	fixture := newHLSFixture(t, "hold")
	manager := fixture.manager(t, observed)
	defer closeHLSManager(t, manager)
	request := fixture.request("concurrent-leases", 4)
	request.Live = true
	if err := manager.StartHLS(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	fixture.waitStarted(t, "concurrent-leases")
	pid := fixture.waitPID(t, "concurrent-leases")
	fixture.writePlaylistText(t, request, "#EXTM3U\n#EXTINF:4,\nconcurrent-leases0.ts\n")
	observed.waitForTimer(t, 60*time.Second)

	segmentContexts := make([]context.CancelFunc, 2)
	segmentResults := make([]chan hlsWaitResult, 2)
	for index := range segmentContexts {
		ctx, cancel := context.WithCancel(context.Background())
		segmentContexts[index] = cancel
		segmentResults[index] = make(chan hlsWaitResult, 1)
		go func(results chan<- hlsWaitResult) {
			file, err := manager.WaitSegment(ctx, request.PlaylistPath, 0)
			results <- hlsWaitResult{file: file, err: err}
		}(segmentResults[index])
	}
	playlistContexts := make([]context.CancelFunc, 2)
	playlistResults := make([]chan hlsPlaylistResult, 2)
	for index := range playlistContexts {
		ctx, cancel := context.WithCancel(context.Background())
		playlistContexts[index] = cancel
		playlistResults[index] = make(chan hlsPlaylistResult, 1)
		go func(results chan<- hlsPlaylistResult) {
			playlist, err := manager.WaitPlaylist(ctx, request.PlaylistPath, 2)
			results <- hlsPlaylistResult{playlist: playlist, err: err}
		}(playlistResults[index])
	}
	observed.waitForTimerCount(t, 100*time.Millisecond, 4)

	base.Advance(60 * time.Second)
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("concurrent HLS waiters lost their shared job to idle shutdown: %v", err)
	}
	for index := range 2 {
		segmentContexts[index]()
		result := receiveHLSWait(t, segmentResults[index])
		if result.file != nil {
			_ = result.file.Close()
		}
		if !errors.Is(result.err, context.Canceled) {
			t.Fatalf("segment waiter %d cancellation = %v, want context.Canceled", index, result.err)
		}
		if index == 0 {
			playlistContexts[0]()
			result := receiveHLSPlaylist(t, playlistResults[0])
			if !errors.Is(result.err, context.Canceled) {
				t.Fatalf("playlist waiter %d cancellation = %v, want context.Canceled", index, result.err)
			}
		}
	}
	base.Advance(60 * time.Second)
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("idle shutdown armed before the final HLS waiter released: %v", err)
	}
	playlistContexts[1]()
	result := receiveHLSPlaylist(t, playlistResults[1])
	if !errors.Is(result.err, context.Canceled) {
		t.Fatalf("final playlist waiter cancellation = %v, want context.Canceled", result.err)
	}
	observed.waitForTimerCount(t, 60*time.Second, 2)
	base.Advance(60 * time.Second)
	fixture.waitQ(t, "concurrent-leases")
	waitForProcessExit(t, pid)
}

func TestRestartHLSAtFollowsCompatibilityRestartBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name       string
		index      int
		withJob    bool
		current    int
		wantStarts int
	}{
		{name: "fMP4 init restarts at zero-safe boundary", index: -1, withJob: true, current: 10, wantStarts: 2},
		{name: "missing job starts requested index", index: 3, wantStarts: 1},
		{name: "backward request restarts", index: 9, withJob: true, current: 10, wantStarts: 2},
		{name: "forward gap at 24 over segment length reuses", index: 16, withJob: true, current: 10, wantStarts: 1},
		{name: "forward gap above 24 over segment length restarts", index: 17, withJob: true, current: 10, wantStarts: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newHLSFixture(t, "hold")
			manager := fixture.manager(t, clock.NewReal())
			defer closeHLSManager(t, manager)
			request := fixture.request("restart", 4)
			if tc.withJob {
				if err := manager.StartHLS(context.Background(), request); err != nil {
					t.Fatal(err)
				}
				fixture.waitStarted(t, "restart")
				fixture.writePlaylist(t, request)
				fixture.writeSegment(t, request, tc.current, "current")
			}

			if err := manager.RestartHLSAt(context.Background(), request, tc.index); err != nil {
				t.Fatalf("RestartHLSAt(%d): %v", tc.index, err)
			}
			if got := len(waitForHLSStarts(t, fixture, "restart", tc.wantStarts)); got != tc.wantStarts {
				t.Fatalf("starts after RestartHLSAt(%d) = %d, want %d", tc.index, got, tc.wantStarts)
			}
		})
	}
}

func TestRestartHLSAtServesRetainedCompletedSegmentWithoutRelaunch(t *testing.T) {
	fixture := newHLSFixture(t, "exit-on-go")
	manager := fixture.manager(t, clock.NewReal())
	defer closeHLSManager(t, manager)
	request := fixture.request("retained-restart", 4)
	if err := manager.StartHLS(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	fixture.waitStarted(t, "retained-restart")
	pid := fixture.waitPID(t, "retained-restart")
	fixture.writePlaylist(t, request)
	fixture.writeSegment(t, request, 0, "cached segment")
	fixture.writeSegment(t, request, 1, "cached successor")
	fixture.release(t, "retained-restart")
	waitForProcessExit(t, pid)
	waitForHLSJobExit(t, manager, request.PlaylistPath)

	if err := manager.RestartHLSAt(context.Background(), request, 0); err != nil {
		t.Fatalf("RestartHLSAt cached segment: %v", err)
	}
	if got := hlsStartCount(t, fixture, "retained-restart"); got != 1 {
		t.Fatalf("cached retained segment restarted ffmpeg %d times, want no relaunch", got)
	}
	file, err := manager.WaitSegment(context.Background(), request.PlaylistPath, 0)
	if err != nil {
		t.Fatalf("WaitSegment cached retained segment: %v", err)
	}
	defer closeHLSSegment(t, file)
	if got := readHLSSegment(t, file); got != "cached segment" {
		t.Fatalf("cached retained segment = %q, want cached segment", got)
	}
	for _, path := range []string{request.PlaylistPath, fixture.segmentPath(request, 0), fixture.segmentPath(request, 1)} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("cached retained HLS artifact %q was deleted: %v", path, err)
		}
	}
}

func TestRestartHLSAtServesActiveCachedBackwardSegmentWithoutRelaunch(t *testing.T) {
	fixture := newHLSFixture(t, "hold")
	manager := fixture.manager(t, clock.NewReal())
	defer closeHLSManager(t, manager)
	request := fixture.request("active-cached-restart", 4)
	if err := manager.StartHLS(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	fixture.waitStarted(t, "active-cached-restart")
	fixture.writePlaylist(t, request)
	fixture.writeSegment(t, request, 0, "cached backward")
	fixture.writeSegment(t, request, 1, "current")

	if err := manager.RestartHLSAt(context.Background(), request, 0); err != nil {
		t.Fatalf("RestartHLSAt cached backward segment: %v", err)
	}
	if got := hlsStartCount(t, fixture, "active-cached-restart"); got != 1 {
		t.Fatalf("active cached backward segment restarted ffmpeg %d times, want no relaunch", got)
	}
	file, err := manager.WaitSegment(context.Background(), request.PlaylistPath, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer closeHLSSegment(t, file)
	if got := readHLSSegment(t, file); got != "cached backward" {
		t.Fatalf("active cached backward segment = %q, want cached backward", got)
	}
	for _, path := range []string{request.PlaylistPath, fixture.segmentPath(request, 0), fixture.segmentPath(request, 1)} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("active cached HLS artifact %q was deleted: %v", path, err)
		}
	}
}

func TestWaitSegmentBodyRetainsLeaseUntilCloseAndAdvancesDownloadPosition(t *testing.T) {
	base := clock.NewFake(time.Date(2026, time.July, 28, 12, 0, 0, 0, time.UTC))
	observed := newProgressiveObservedClock(base)
	fixture := newHLSFixture(t, "hold")
	manager := fixture.manager(t, observed)
	defer closeHLSManager(t, manager)
	request := fixture.request("body-lease", 4)
	if err := manager.StartHLS(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	fixture.waitStarted(t, "body-lease")
	pid := fixture.waitPID(t, "body-lease")
	fixture.writePlaylist(t, request)
	fixture.writeSegment(t, request, 0, "body")
	fixture.writeSegment(t, request, 1, "successor")
	file, err := manager.WaitSegment(context.Background(), request.PlaylistPath, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := readHLSSegment(t, file); got != "body" {
		_ = file.Close()
		t.Fatalf("segment body = %q, want body", got)
	}
	base.Advance(60 * time.Second)
	if err := syscall.Kill(pid, 0); err != nil {
		_ = file.Close()
		t.Fatalf("idle shutdown killed HLS while returned segment body remained open: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	canonical, err := manager.normalizeOutputPath(request.PlaylistPath)
	if err != nil {
		t.Fatal(err)
	}
	manager.mu.Lock()
	position := manager.jobs[canonical].positionTicks
	manager.mu.Unlock()
	if want := int64(4 * time.Second / 100); position != want {
		t.Fatalf("download position ticks after segment body Close = %d, want %d", position, want)
	}
	observed.waitForTimerCount(t, 60*time.Second, 2)
	base.Advance(60 * time.Second)
	fixture.waitQ(t, "body-lease")
	waitForProcessExit(t, pid)
}

func TestWaitSegmentRebindsLeaseAfterConcurrentRestartEpoch(t *testing.T) {
	fixture := newHLSFixture(t, "hold")
	manager := fixture.manager(t, clock.NewReal())
	defer closeHLSManager(t, manager)
	requestA := fixture.request("wait-rebind-a", 4)
	if err := manager.StartHLS(context.Background(), requestA); err != nil {
		t.Fatal(err)
	}
	fixture.waitStarted(t, "wait-rebind-a")
	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce, hookOnce sync.Once
	manager.beforeHLSWaitRetain = func() {
		hookOnce.Do(func() {
			close(entered)
			<-release
		})
	}
	t.Cleanup(func() {
		manager.beforeHLSWaitRetain = nil
		releaseOnce.Do(func() { close(release) })
	})
	waits := make(chan hlsWaitResult, 1)
	go func() {
		file, err := manager.WaitSegment(context.Background(), requestA.PlaylistPath, 0)
		waits <- hlsWaitResult{file: file, err: err}
	}()
	waitForProgressiveSignal(t, entered, "WaitSegment snapshot barrier")
	requestB := requestA
	requestB.Args = []string{"--fixture-label=wait-rebind-b", "-hls_segment_filename", requestA.SegmentPattern, requestA.PlaylistPath}
	restarted := make(chan error, 1)
	go func() { restarted <- manager.RestartHLSAt(context.Background(), requestB, 0) }()
	fixture.waitStarted(t, "wait-rebind-b")
	fixture.writePlaylist(t, requestB)
	fixture.writeSegment(t, requestB, 0, "partial B")
	if err := receiveProgressiveError(t, restarted); err != nil {
		t.Fatalf("RestartHLSAt B: %v", err)
	}
	releaseOnce.Do(func() { close(release) })
	assertHLSPending(t, waits)
	fixture.writeSegment(t, requestB, 1, "B successor")
	file := receiveHLSFile(t, waits)
	if got := readHLSSegment(t, file); got != "partial B" {
		_ = file.Close()
		t.Fatalf("rebound segment body = %q, want B generation bytes", got)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	canonical, err := manager.normalizeOutputPath(requestB.PlaylistPath)
	if err != nil {
		t.Fatal(err)
	}
	manager.mu.Lock()
	job := manager.jobs[canonical]
	downloadPosition := job.downloadPositionTicks
	manager.mu.Unlock()
	if !reflect.DeepEqual(job.args, requestB.Args) {
		t.Fatalf("WaitSegment retained lease for stale argv %#v, want B argv %#v", job.args, requestB.Args)
	}
	if want := int64(4 * time.Second / 100); downloadPosition != want {
		t.Fatalf("rebound WaitSegment download ticks = %d, want %d on B job", downloadPosition, want)
	}
}

func TestWaitSegmentSurvivesRestartReplacementRegistrationGap(t *testing.T) {
	t.Run("replacement registration returns B generation bytes", func(t *testing.T) {
		fixture := newHLSFixture(t, "hold")
		manager := fixture.manager(t, clock.NewReal())
		defer closeHLSManager(t, manager)
		requestA := fixture.request("wait-registration-gap-a", 4)
		if err := manager.StartHLS(context.Background(), requestA); err != nil {
			t.Fatal(err)
		}
		fixture.waitStarted(t, "wait-registration-gap-a")
		pidA := fixture.waitPID(t, "wait-registration-gap-a")

		waitEntered := make(chan struct{})
		waitRelease := make(chan struct{})
		registrationEntered := make(chan struct{})
		registrationRelease := make(chan struct{})
		var waitReleaseOnce, registrationReleaseOnce sync.Once
		manager.beforeHLSWaitRetain = func() {
			close(waitEntered)
			<-waitRelease
		}
		manager.beforeHLSRestartRegister = func() {
			close(registrationEntered)
			<-registrationRelease
		}
		t.Cleanup(func() {
			manager.beforeHLSWaitRetain = nil
			manager.beforeHLSRestartRegister = nil
			waitReleaseOnce.Do(func() { close(waitRelease) })
			registrationReleaseOnce.Do(func() { close(registrationRelease) })
		})

		waits := make(chan hlsWaitResult, 1)
		go func() {
			file, err := manager.WaitSegment(context.Background(), requestA.PlaylistPath, 0)
			waits <- hlsWaitResult{file: file, err: err}
		}()
		waitForProgressiveSignal(t, waitEntered, "A-bound WaitSegment snapshot")
		waitReleaseOnce.Do(func() { close(waitRelease) })

		requestB := requestA
		requestB.Args = []string{"--fixture-label=wait-registration-gap-b"}
		restarted := make(chan error, 1)
		go func() { restarted <- manager.RestartHLSAt(context.Background(), requestB, 0) }()
		waitForProgressiveSignal(t, registrationEntered, "replacement registration gap")
		waitForProcessExit(t, pidA)
		canonicalPlaylist, err := manager.normalizeOutputPath(requestA.PlaylistPath)
		if err != nil {
			t.Fatal(err)
		}
		manager.mu.Lock()
		job := manager.jobs[canonicalPlaylist]
		manager.mu.Unlock()
		if job != nil {
			t.Fatal("A generation remained registered while RestartHLSAt was held before B registration")
		}
		assertHLSPending(t, waits)

		registrationReleaseOnce.Do(func() { close(registrationRelease) })
		fixture.waitStarted(t, "wait-registration-gap-b")
		fixture.writePlaylist(t, requestB)
		fixture.writeSegment(t, requestB, 0, "B generation segment")
		if err := receiveProgressiveError(t, restarted); err != nil {
			t.Fatalf("RestartHLSAt B: %v", err)
		}
		fixture.writeSegment(t, requestB, 1, "B generation successor")
		file := receiveHLSFile(t, waits)
		if got := readHLSSegment(t, file); got != "B generation segment" {
			_ = file.Close()
			t.Fatalf("rebound segment bytes = %q, want B generation bytes", got)
		}
		closeHLSSegment(t, file)
	})

	t.Run("restart without B registration terminates waiter", func(t *testing.T) {
		fixture := newHLSFixture(t, "hold")
		manager := fixture.manager(t, clock.NewReal())
		defer closeHLSManager(t, manager)
		requestA := fixture.request("wait-registration-failure-a", 4)
		if err := manager.StartHLS(context.Background(), requestA); err != nil {
			t.Fatal(err)
		}
		fixture.waitStarted(t, "wait-registration-failure-a")
		pidA := fixture.waitPID(t, "wait-registration-failure-a")

		waitEntered := make(chan struct{})
		waitRelease := make(chan struct{})
		registrationEntered := make(chan struct{})
		registrationRelease := make(chan struct{})
		var waitReleaseOnce, registrationReleaseOnce sync.Once
		manager.beforeHLSWaitRetain = func() {
			close(waitEntered)
			<-waitRelease
		}
		manager.beforeHLSRestartRegister = func() {
			close(registrationEntered)
			<-registrationRelease
		}
		t.Cleanup(func() {
			manager.beforeHLSWaitRetain = nil
			manager.beforeHLSRestartRegister = nil
			waitReleaseOnce.Do(func() { close(waitRelease) })
			registrationReleaseOnce.Do(func() { close(registrationRelease) })
		})

		waits := make(chan hlsWaitResult, 1)
		go func() {
			file, err := manager.WaitSegment(context.Background(), requestA.PlaylistPath, 0)
			waits <- hlsWaitResult{file: file, err: err}
		}()
		waitForProgressiveSignal(t, waitEntered, "A-bound WaitSegment snapshot")
		waitReleaseOnce.Do(func() { close(waitRelease) })

		requestB := requestA
		requestB.Args = []string{"--fixture-label=wait-registration-failure-b"}
		restarted := make(chan error, 1)
		go func() { restarted <- manager.RestartHLSAt(context.Background(), requestB, 0) }()
		waitForProgressiveSignal(t, registrationEntered, "failed replacement registration gap")
		waitForProcessExit(t, pidA)
		if err := manager.StopPlaySession(context.Background(), requestA.PlaySessionID, requestA.DeviceID); err != nil {
			t.Fatalf("StopPlaySession during replacement registration gap: %v", err)
		}
		registrationReleaseOnce.Do(func() { close(registrationRelease) })
		if err := receiveProgressiveError(t, restarted); err == nil {
			t.Fatal("RestartHLSAt unexpectedly registered B after StopPlaySession")
		}
		result := receiveHLSWait(t, waits)
		if result.file != nil {
			_ = result.file.Close()
			t.Fatal("WaitSegment returned stale bytes after the replacement restart failed")
		}
		if result.err == nil {
			t.Fatal("WaitSegment waited forever or succeeded after the replacement restart failed")
		}
	})
}

func TestWaitPlaylistReturnsLargeLiveEventManifestVerbatim(t *testing.T) {
	fixture := newHLSFixture(t, "hold")
	manager := fixture.manager(t, clock.NewReal())
	defer closeHLSManager(t, manager)
	request := fixture.request("large-event-playlist", 4)
	request.Live = true
	request.MinSegments = 1
	if err := manager.StartHLS(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	fixture.waitStarted(t, "large-event-playlist")

	want := "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-PLAYLIST-TYPE:EVENT\n#EXTINF:4,\nsegment.ts?opaque=" + strings.Repeat("x", 1<<20) + "\n"
	if len(want) <= 1<<20 {
		t.Fatalf("EVENT manifest size = %d, want more than one MiB", len(want))
	}
	fixture.writePlaylistText(t, request, want)
	got, err := manager.WaitPlaylist(context.Background(), request.PlaylistPath, 1)
	if err != nil {
		t.Fatalf("WaitPlaylist large EVENT manifest: %v", err)
	}
	if got != want {
		t.Fatalf("WaitPlaylist large EVENT manifest returned %d bytes, want all %d bytes", len(got), len(want))
	}
}

func TestRestartHLSAtColdRequestStartsExactlyOneJob(t *testing.T) {
	fixture := newHLSFixture(t, "hold")
	manager := fixture.manager(t, clock.NewReal())
	defer closeHLSManager(t, manager)
	request := fixture.request("cold-restart", 4)

	if err := manager.RestartHLSAt(context.Background(), request, 7); err != nil {
		t.Fatalf("cold RestartHLSAt: %v", err)
	}
	if got := len(waitForHLSStarts(t, fixture, "cold-restart", 1)); got != 1 {
		t.Fatalf("cold RestartHLSAt starts = %d, want one", got)
	}
}

func TestRestartHLSAtCoalescesQueuedIdenticalSeeksUntilRequestedProgress(t *testing.T) {
	fixture := newHLSFixture(t, "hold")
	manager := fixture.manager(t, clock.NewReal())
	defer closeHLSManager(t, manager)
	oldRequest := fixture.request("storm-old", 4)
	if err := manager.StartHLS(context.Background(), oldRequest); err != nil {
		t.Fatal(err)
	}
	fixture.waitStarted(t, "storm-old")
	fixture.writePlaylist(t, oldRequest)
	fixture.writeSegment(t, oldRequest, 10, "old current")

	request := oldRequest
	request.Args = []string{"--fixture-label=storm-new", "-ss", "new-requested-seek", "-start_number", "9"}
	t.Setenv("HLS_AUTO_SEGMENT_WAIT", fixture.marker("go", "storm-new"))
	t.Setenv("HLS_AUTO_SEGMENT", fixture.segmentPath(request, 9))

	const callers = 24
	errs := make(chan error, callers)
	var start sync.WaitGroup
	start.Add(1)
	for range callers {
		go func() {
			start.Wait()
			errs <- manager.RestartHLSAt(context.Background(), request, 9)
		}()
	}
	start.Done()
	fixture.waitStarted(t, "storm-new")
	assertHLSRestartPending(t, errs)
	fixture.release(t, "storm-new")
	for range callers {
		if err := <-errs; err != nil {
			t.Fatalf("queued RestartHLSAt: %v", err)
		}
	}
	if got := len(waitForHLSStarts(t, fixture, "storm-old", 1)); got != 1 {
		t.Fatalf("old seek starts = %d, want one original process", got)
	}
	if got := len(waitForHLSStarts(t, fixture, "storm-new", 1)); got != 1 {
		t.Fatalf("identical queued seeks started %d replacements, want one", got)
	}
	if got, want := readLines(t, fixture.marker("argv", "storm-new")), request.Args; !reflect.DeepEqual(got, want) {
		t.Fatalf("replacement argv = %#v, want requested seek argv %#v", got, want)
	}
}

func TestStartHLSRejectsMismatchedAndOutsideOutputOperands(t *testing.T) {
	fixture := newHLSFixture(t, "hold")
	manager := fixture.manager(t, clock.NewReal())
	defer closeHLSManager(t, manager)
	request := fixture.request("operand", 4)
	insideOtherPlaylist := filepath.Join(fixture.dir, "other.m3u8")
	insideOtherPattern := filepath.Join(fixture.dir, "other%d.ts")
	outside := t.TempDir()
	for _, tc := range []struct {
		name string
		args []string
	}{
		{name: "mismatched segment pattern", args: []string{"--fixture-label=operand", "-hls_segment_filename", insideOtherPattern, request.PlaylistPath}},
		{name: "mismatched playlist", args: []string{"--fixture-label=operand", "-hls_segment_filename", request.SegmentPattern, insideOtherPlaylist}},
		{name: "outside segment pattern", args: []string{"--fixture-label=operand", "-hls_segment_filename", filepath.Join(outside, "outside%d.ts"), request.PlaylistPath}},
		{name: "outside playlist", args: []string{"--fixture-label=operand", "-hls_segment_filename", request.SegmentPattern, filepath.Join(outside, "outside.m3u8")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := request
			candidate.Args = tc.args
			if err := manager.StartHLS(context.Background(), candidate); err == nil {
				t.Fatal("StartHLS accepted mismatched or outside output operands")
			}
			if _, err := os.Stat(fixture.marker("started", "operand")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("invalid HLS argv started ffmpeg: %v", err)
			}
		})
	}
}

func TestStartHLSRejectsOutputPathRetargetedThroughConstructorAlias(t *testing.T) {
	fixture := newHLSFixture(t, "hold")
	root := t.TempDir()
	owned, outside := filepath.Join(root, "owned"), filepath.Join(root, "outside")
	if err := os.MkdirAll(owned, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(owned, alias); err != nil {
		t.Fatal(err)
	}
	manager, err := NewTranscodeManager(fixture.ffmpeg, filepath.Join(alias, "transcodes"), TranscodeManagerOptions{
		Clock:                  clock.NewReal(),
		OutputPollInterval:     100 * time.Millisecond,
		ReaderRetryInterval:    50 * time.Millisecond,
		ReaderStallTimeout:     30 * time.Second,
		ProgressiveIdleTimeout: 10 * time.Second,
		KillGracePeriod:        5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer closeHLSManager(t, manager)
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, alias); err != nil {
		t.Fatal(err)
	}
	playlist := filepath.Join(alias, "transcodes", "retargeted.m3u8")
	pattern := filepath.Join(alias, "transcodes", "retargeted%d.ts")
	request := HLSRequest{
		PlaylistPath:         playlist,
		SegmentPattern:       pattern,
		Args:                 []string{"--fixture-label=retargeted", "-hls_segment_filename", pattern, playlist},
		SegmentLengthSeconds: 4,
	}
	if err := manager.StartHLS(context.Background(), request); err == nil {
		t.Fatal("StartHLS accepted output paths retargeted through a constructor alias")
	}
	for _, path := range []string{filepath.Join(outside, "transcodes", "retargeted.m3u8"), filepath.Join(outside, "transcodes", "retargeted0.ts")} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("retargeted HLS alias wrote outside the owned directory at %q: %v", path, err)
		}
	}
}

func TestRetiredHLSSessionsReleaseJobsSessionIndexAndPlaylistLocks(t *testing.T) {
	fixture := newHLSFixture(t, "hold")
	manager := fixture.manager(t, clock.NewReal())
	defer closeHLSManager(t, manager)
	for index := range 128 {
		label := fmt.Sprintf("retired-%d", index)
		request := fixture.request(label, 4)
		if err := manager.RestartHLSAt(context.Background(), request, 0); err != nil {
			t.Fatalf("RestartHLSAt(%q): %v", label, err)
		}
		fixture.waitStarted(t, label)
		if err := manager.StopPlaySession(context.Background(), request.PlaySessionID, request.DeviceID); err != nil {
			t.Fatalf("StopPlaySession(%q): %v", label, err)
		}
	}
	manager.mu.Lock()
	jobs, sessions, locks, epochs := len(manager.jobs), len(manager.hlsSessions), len(manager.hlsLocks), len(manager.hlsSessionEpoch)
	manager.mu.Unlock()
	if jobs != 0 || sessions != 0 || locks != 0 || epochs != 0 {
		t.Fatalf("retired HLS state leaked: jobs=%d sessions=%d locks=%d epochs=%d, want all zero", jobs, sessions, locks, epochs)
	}
}

func TestHLSIdleExpiryIsSixtySecondsAndPingResetsTheSession(t *testing.T) {
	base := clock.NewFake(time.Date(2026, time.July, 28, 12, 0, 0, 0, time.UTC))
	fixture := newHLSFixture(t, "hold")
	manager := fixture.manager(t, base)
	defer closeHLSManager(t, manager)
	request := fixture.request("idle", 4)
	request.PlaySessionID = "opaque/session?one"
	if err := manager.StartHLS(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	fixture.waitStarted(t, "idle")
	pid := fixture.waitPID(t, "idle")

	base.Advance(59 * time.Second)
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("HLS job exited before its 60-second inactivity boundary: %v", err)
	}
	if !manager.PingPlaySession(request.PlaySessionID) {
		t.Fatal("PingPlaySession did not find the active opaque session")
	}
	base.Advance(time.Second)
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("PingPlaySession did not reset HLS inactivity: %v", err)
	}
	base.Advance(59 * time.Second)
	fixture.waitQ(t, "idle")
	waitForProcessExit(t, pid)
}

func TestStopPlaySessionSelectsOnlyExactOpaqueSessionAndDevice(t *testing.T) {
	fixture := newHLSFixture(t, "hold")
	manager := fixture.manager(t, clock.NewReal())
	defer closeHLSManager(t, manager)
	wanted := fixture.request("wanted", 4)
	wanted.PlaySessionID = "opaque/session?one"
	wanted.DeviceID = "device/one"
	other := fixture.request("other", 4)
	other.PlaySessionID = wanted.PlaySessionID
	other.DeviceID = "device/two"
	if err := manager.StartHLS(context.Background(), wanted); err != nil {
		t.Fatal(err)
	}
	if err := manager.StartHLS(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	fixture.waitStarted(t, "wanted")
	fixture.waitStarted(t, "other")
	wantedPID, otherPID := fixture.waitPID(t, "wanted"), fixture.waitPID(t, "other")

	if err := manager.StopPlaySession(context.Background(), wanted.PlaySessionID, wanted.DeviceID); err != nil {
		t.Fatalf("StopPlaySession: %v", err)
	}
	fixture.waitQ(t, "wanted")
	waitForProcessExit(t, wantedPID)
	if err := syscall.Kill(otherPID, 0); err != nil {
		t.Fatalf("StopPlaySession selected non-matching opaque device: %v", err)
	}
}

func TestStopPlaySessionCleansRetainedSuccessfulHLSFamily(t *testing.T) {
	fixture := newHLSFixture(t, "exit-on-go")
	manager := fixture.manager(t, clock.NewReal())
	defer closeHLSManager(t, manager)
	request := fixture.request("retained-stop", 4)
	if err := manager.RestartHLSAt(context.Background(), request, 0); err != nil {
		t.Fatal(err)
	}
	fixture.waitStarted(t, "retained-stop")
	pid := fixture.waitPID(t, "retained-stop")
	fixture.writePlaylist(t, request)
	fixture.writeSegment(t, request, 0, "retained")
	fixture.release(t, "retained-stop")
	waitForProcessExit(t, pid)
	waitForHLSJobExit(t, manager, request.PlaylistPath)

	if err := manager.StopPlaySession(context.Background(), request.PlaySessionID, request.DeviceID); err != nil {
		t.Fatalf("StopPlaySession retained HLS family: %v", err)
	}
	for _, path := range []string{request.PlaylistPath, fixture.segmentPath(request, 0)} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("StopPlaySession left retained HLS artifact %q: %v", path, err)
		}
	}
	manager.mu.Lock()
	jobs, sessions, locks := len(manager.jobs), len(manager.hlsSessions), len(manager.hlsLocks)
	manager.mu.Unlock()
	if jobs != 0 || sessions != 0 || locks != 0 {
		t.Fatalf("StopPlaySession left retained HLS indexes: jobs=%d sessions=%d locks=%d", jobs, sessions, locks)
	}
}

func TestStopPlaySessionWinsAgainstBlockedRestartBeforeReplacement(t *testing.T) {
	fixture := newHLSFixture(t, "hold")
	manager := fixture.manager(t, clock.NewReal())
	defer closeHLSManager(t, manager)
	request := fixture.request("stop-restart", 4)
	if err := manager.StartHLS(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	fixture.waitStarted(t, "stop-restart")
	fixture.writePlaylist(t, request)
	fixture.writeSegment(t, request, 10, "current")
	entered := make(chan struct{})
	release := make(chan struct{})
	manager.beforeHLSRestartStart = func() {
		close(entered)
		<-release
	}
	t.Cleanup(func() { manager.beforeHLSRestartStart = nil })
	restarted := make(chan error, 1)
	go func() { restarted <- manager.RestartHLSAt(context.Background(), request, 9) }()
	waitForProgressiveSignal(t, entered, "restart replacement barrier")
	if err := manager.StopPlaySession(context.Background(), request.PlaySessionID, request.DeviceID); err != nil {
		t.Fatalf("StopPlaySession during restart: %v", err)
	}
	close(release)
	if err := receiveProgressiveError(t, restarted); err == nil {
		t.Fatal("RestartHLSAt succeeded after StopPlaySession invalidated the session")
	}
	if got := hlsStartCount(t, fixture, "stop-restart"); got != 1 {
		t.Fatalf("StopPlaySession allowed replacement start count %d, want original only", got)
	}
	manager.mu.Lock()
	jobs, sessions := len(manager.jobs), len(manager.hlsSessions)
	manager.mu.Unlock()
	if jobs != 0 || sessions != 0 {
		t.Fatalf("StopPlaySession left replacement state: jobs=%d sessions=%d", jobs, sessions)
	}
}

func TestStopPlaySessionPreventsBlockedColdRestartFromStarting(t *testing.T) {
	fixture := newHLSFixture(t, "hold")
	manager := fixture.manager(t, clock.NewReal())
	defer closeHLSManager(t, manager)
	request := fixture.request("cold-stop-restart", 4)
	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	manager.beforeHLSRestartStart = func() {
		close(entered)
		<-release
	}
	t.Cleanup(func() {
		manager.beforeHLSRestartStart = nil
		releaseOnce.Do(func() { close(release) })
	})
	restarted := make(chan error, 1)
	go func() { restarted <- manager.RestartHLSAt(context.Background(), request, 0) }()
	waitForProgressiveSignal(t, entered, "cold restart barrier")
	if err := manager.StopPlaySession(context.Background(), request.PlaySessionID, request.DeviceID); err != nil {
		t.Fatalf("StopPlaySession during cold restart: %v", err)
	}
	releaseOnce.Do(func() { close(release) })
	if err := receiveProgressiveError(t, restarted); err == nil {
		t.Fatal("cold RestartHLSAt succeeded after StopPlaySession")
	}
	if got := hlsStartCount(t, fixture, "cold-stop-restart"); got != 0 {
		t.Fatalf("cold RestartHLSAt started %d processes after StopPlaySession, want none", got)
	}
	manager.mu.Lock()
	jobs, sessions, locks := len(manager.jobs), len(manager.hlsSessions), len(manager.hlsLocks)
	manager.mu.Unlock()
	if jobs != 0 || sessions != 0 || locks != 0 {
		t.Fatalf("cold StopPlaySession left state: jobs=%d sessions=%d locks=%d", jobs, sessions, locks)
	}
}

func TestStopPlaySessionInvalidatesBlockedCachedRestart(t *testing.T) {
	for _, tc := range []struct {
		name     string
		mode     string
		retained bool
	}{
		{name: "active cache hit", mode: "hold"},
		{name: "retained cache hit", mode: "exit-on-go", retained: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newHLSFixture(t, tc.mode)
			manager := fixture.manager(t, clock.NewReal())
			defer closeHLSManager(t, manager)
			request := fixture.request("cached-stop-"+strings.ReplaceAll(tc.name, " ", "-"), 4)
			if err := manager.StartHLS(context.Background(), request); err != nil {
				t.Fatal(err)
			}
			label := filepath.Base(strings.TrimSuffix(request.PlaylistPath, filepath.Ext(request.PlaylistPath)))
			fixture.waitStarted(t, label)
			fixture.writePlaylist(t, request)
			fixture.writeSegment(t, request, 0, "cached")
			fixture.writeSegment(t, request, 1, "successor")
			if tc.retained {
				pid := fixture.waitPID(t, label)
				fixture.release(t, label)
				waitForProcessExit(t, pid)
				waitForHLSJobExit(t, manager, request.PlaylistPath)
			}
			entered := make(chan struct{})
			release := make(chan struct{})
			var releaseOnce sync.Once
			manager.beforeHLSRestartStart = func() {
				close(entered)
				<-release
			}
			t.Cleanup(func() {
				manager.beforeHLSRestartStart = nil
				releaseOnce.Do(func() { close(release) })
			})
			restarted := make(chan error, 1)
			go func() { restarted <- manager.RestartHLSAt(context.Background(), request, 0) }()
			waitForProgressiveSignal(t, entered, tc.name+" barrier")
			if err := manager.StopPlaySession(context.Background(), request.PlaySessionID, request.DeviceID); err != nil {
				t.Fatalf("StopPlaySession during %s: %v", tc.name, err)
			}
			releaseOnce.Do(func() { close(release) })
			if err := receiveProgressiveError(t, restarted); err == nil {
				t.Fatalf("cached RestartHLSAt succeeded after StopPlaySession (%s)", tc.name)
			}
			if got := hlsStartCount(t, fixture, label); got != 1 {
				t.Fatalf("cached RestartHLSAt launched %d processes after StopPlaySession (%s), want original only", got, tc.name)
			}
			manager.mu.Lock()
			jobs, sessions, locks := len(manager.jobs), len(manager.hlsSessions), len(manager.hlsLocks)
			manager.mu.Unlock()
			if jobs != 0 || sessions != 0 || locks != 0 {
				t.Fatalf("cached StopPlaySession left state (%s): jobs=%d sessions=%d locks=%d", tc.name, jobs, sessions, locks)
			}
		})
	}
}

func TestStopPlaySessionInvalidatesRestartAfterEpochConfirmationBeforeRegistration(t *testing.T) {
	fixture := newHLSFixture(t, "hold")
	manager := fixture.manager(t, clock.NewReal())
	defer closeHLSManager(t, manager)
	request := fixture.request("post-confirm-stop", 4)
	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	manager.beforeHLSRestartRegister = func() {
		close(entered)
		<-release
	}
	t.Cleanup(func() {
		manager.beforeHLSRestartRegister = nil
		releaseOnce.Do(func() { close(release) })
	})
	restarted := make(chan error, 1)
	go func() { restarted <- manager.RestartHLSAt(context.Background(), request, 0) }()
	waitForProgressiveSignal(t, entered, "post-confirm restart registration barrier")
	if err := manager.StopPlaySession(context.Background(), request.PlaySessionID, request.DeviceID); err != nil {
		t.Fatalf("StopPlaySession after restart epoch confirmation: %v", err)
	}
	releaseOnce.Do(func() { close(release) })
	if err := receiveProgressiveError(t, restarted); err == nil {
		t.Fatal("RestartHLSAt succeeded after StopPlaySession crossed the post-confirm registration barrier")
	}
	if got := hlsStartCount(t, fixture, "post-confirm-stop"); got != 0 {
		t.Fatalf("post-confirm RestartHLSAt started %d processes after StopPlaySession, want none", got)
	}
	manager.mu.Lock()
	jobs, sessions, locks := len(manager.jobs), len(manager.hlsSessions), len(manager.hlsLocks)
	manager.mu.Unlock()
	if jobs != 0 || sessions != 0 || locks != 0 {
		t.Fatalf("post-confirm StopPlaySession left restart state: jobs=%d sessions=%d locks=%d", jobs, sessions, locks)
	}
}

func TestHLSPostPreparationStartupFailuresCleanArtifactsAndIndexes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		inject func(*TranscodeManager, error)
	}{
		{name: "sidecar log", inject: func(manager *TranscodeManager, sentinel error) {
			manager.hlsLogOpen = func(*transcodeJob) (*os.File, error) { return nil, sentinel }
		}},
		{name: "stdin pipe", inject: func(manager *TranscodeManager, sentinel error) {
			manager.stdinPipe = func(*exec.Cmd) (io.WriteCloser, error) { return nil, sentinel }
		}},
		{name: "process start", inject: func(manager *TranscodeManager, sentinel error) {
			manager.startCommand = func(*exec.Cmd) error { return sentinel }
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newHLSFixture(t, "hold")
			manager := fixture.manager(t, clock.NewReal())
			defer closeHLSManager(t, manager)
			sentinel := errors.New("injected " + tc.name + " failure")
			tc.inject(manager, sentinel)
			request := fixture.request("startup-"+strings.ReplaceAll(tc.name, " ", "-"), 4)
			if err := manager.StartHLS(context.Background(), request); !errors.Is(err, sentinel) {
				t.Fatalf("StartHLS error = %v, want injected %v", err, sentinel)
			}
			for _, path := range []string{request.PlaylistPath, fixture.segmentPath(request, 0), request.PlaylistPath + ".stderr.log"} {
				if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("failed HLS start left artifact %q: %v", path, err)
				}
			}
			manager.mu.Lock()
			jobs, sessions := len(manager.jobs), len(manager.hlsSessions)
			manager.mu.Unlock()
			if jobs != 0 || sessions != 0 {
				t.Fatalf("failed HLS start left indexes: jobs=%d sessions=%d", jobs, sessions)
			}
		})
	}
}

func TestWaitSegmentCancellationDoesNotKillSharedHLSJob(t *testing.T) {
	fixture := newHLSFixture(t, "hold")
	manager := fixture.manager(t, clock.NewReal())
	defer closeHLSManager(t, manager)
	request := fixture.request("cancel", 4)
	if err := manager.StartHLS(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	fixture.waitStarted(t, "cancel")
	pid := fixture.waitPID(t, "cancel")
	fixture.writePlaylist(t, request)

	ctx, cancel := context.WithCancel(context.Background())
	results := make(chan hlsWaitResult, 1)
	go func() {
		file, err := manager.WaitSegment(ctx, request.PlaylistPath, 0)
		results <- hlsWaitResult{file: file, err: err}
	}()
	cancel()
	result := receiveHLSWait(t, results)
	if result.file != nil {
		_ = result.file.Close()
	}
	if !errors.Is(result.err, context.Canceled) {
		t.Fatalf("WaitSegment cancellation = %v, want context.Canceled", result.err)
	}
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("request cancellation killed the shared HLS job: %v", err)
	}

	fixture.writeSegment(t, request, 0, "shared remains live")
	fixture.writeSegment(t, request, 1, "next")
	file, err := manager.WaitSegment(context.Background(), request.PlaylistPath, 0)
	if err != nil {
		t.Fatalf("WaitSegment after canceled request: %v", err)
	}
	defer closeHLSSegment(t, file)
	if got := readHLSSegment(t, file); got != "shared remains live" {
		t.Fatalf("shared HLS job no longer served segment after cancellation: %q", got)
	}
}

func TestWaitSegmentSupportsFMP4InitializationIndexMinusOne(t *testing.T) {
	fixture := newHLSFixture(t, "exit-on-go")
	manager := fixture.manager(t, clock.NewReal())
	defer closeHLSManager(t, manager)
	request := fixture.requestWithExtension("init", 4, ".mp4")
	if err := manager.StartHLS(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	fixture.waitStarted(t, "init")
	pid := fixture.waitPID(t, "init")
	fixture.writePlaylist(t, request)
	fixture.writeSegment(t, request, -1, "fMP4 initialization")
	fixture.release(t, "init")
	waitForProcessExit(t, pid)

	file, err := manager.WaitSegment(context.Background(), request.PlaylistPath, -1)
	if err != nil {
		t.Fatalf("WaitSegment(-1): %v", err)
	}
	defer closeHLSSegment(t, file)
	if got := readHLSSegment(t, file); got != "fMP4 initialization" {
		t.Fatalf("fMP4 initialization bytes = %q", got)
	}
}

func TestOpenLegacyHLSOpensOnlyAssociatedActiveHLSFamily(t *testing.T) {
	fixture := newHLSFixture(t, "hold")
	manager := fixture.manager(t, clock.NewReal())
	defer closeHLSManager(t, manager)
	alpha, beta := fixture.request("alpha", 4), fixture.request("beta", 4)
	for _, request := range []HLSRequest{alpha, beta} {
		if err := manager.StartHLS(context.Background(), request); err != nil {
			t.Fatal(err)
		}
	}
	fixture.waitStarted(t, "alpha")
	fixture.waitStarted(t, "beta")
	fixture.writePlaylist(t, alpha)
	fixture.writeSegment(t, alpha, 0, "alpha segment")
	fixture.writePlaylist(t, beta)
	fixture.writeSegment(t, beta, 0, "beta segment")

	file, err := manager.OpenLegacyHLS(context.Background(), "alpha", "alpha0.ts")
	if err != nil {
		t.Fatalf("OpenLegacyHLS associated segment: %v", err)
	}
	if got := readHLSSegment(t, file); got != "alpha segment" {
		_ = file.Close()
		t.Fatalf("associated legacy segment = %q, want alpha segment", got)
	}
	closeHLSSegment(t, file)

	for _, tc := range []struct {
		name       string
		playlistID string
		artifact   string
	}{
		{name: "foreign family", playlistID: "alpha", artifact: "beta0.ts"},
		{name: "missing family artifact", playlistID: "alpha", artifact: "alpha99.ts"},
		{name: "unknown playlist", playlistID: "missing", artifact: "alpha0.ts"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file, err := manager.OpenLegacyHLS(context.Background(), tc.playlistID, tc.artifact)
			if file != nil {
				_ = file.Close()
			}
			if err == nil {
				t.Fatal("OpenLegacyHLS accepted a missing or foreign artifact")
			}
		})
	}
}

func TestOpenLegacyHLSMapsOnlyAbsentArtifactsToNotExist(t *testing.T) {
	t.Run("absent family and artifact", func(t *testing.T) {
		fixture := newHLSFixture(t, "hold")
		manager := fixture.manager(t, clock.NewReal())
		defer closeHLSManager(t, manager)
		request := fixture.request("known", 4)
		if err := manager.StartHLS(context.Background(), request); err != nil {
			t.Fatal(err)
		}
		fixture.waitStarted(t, "known")
		fixture.writePlaylist(t, request)
		fixture.writeSegment(t, request, 0, "known")

		for _, tc := range []struct {
			name       string
			playlistID string
			artifact   string
		}{
			{name: "absent family", playlistID: "absent", artifact: "absent0.ts"},
			{name: "foreign family artifact", playlistID: "known", artifact: "foreign0.ts"},
			{name: "missing matching artifact", playlistID: "known", artifact: "known99.ts"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				file, err := manager.OpenLegacyHLS(context.Background(), tc.playlistID, tc.artifact)
				if file != nil {
					_ = file.Close()
				}
				if !errors.Is(err, fs.ErrNotExist) {
					t.Fatalf("OpenLegacyHLS(%q, %q) error = %v, want fs.ErrNotExist", tc.playlistID, tc.artifact, err)
				}
			})
		}

		file, err := manager.OpenLegacyHLS(context.Background(), "known", "../known0.ts")
		if file != nil {
			_ = file.Close()
		}
		if err == nil || errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("unsafe legacy token error = %v, want validation error outside fs.ErrNotExist", err)
		}

		outside := filepath.Join(t.TempDir(), "outside.ts")
		if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
			t.Fatal(err)
		}
		segment := fixture.segmentPath(request, 0)
		if err := os.Remove(segment); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, segment); err != nil {
			t.Fatal(err)
		}
		file, err = manager.OpenLegacyHLS(context.Background(), "known", "known0.ts")
		if file != nil {
			_ = file.Close()
		}
		if err == nil || errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("active selected-family open error = %v, want preserved non-not-found error", err)
		}
	})

	t.Run("retained exited selected family", func(t *testing.T) {
		fixture := newHLSFixture(t, "exit-on-go")
		manager := fixture.manager(t, clock.NewReal())
		defer closeHLSManager(t, manager)
		request := fixture.request("retained-error", 4)
		if err := manager.StartHLS(context.Background(), request); err != nil {
			t.Fatal(err)
		}
		fixture.waitStarted(t, "retained-error")
		pid := fixture.waitPID(t, "retained-error")
		fixture.writePlaylist(t, request)
		fixture.writeSegment(t, request, 0, "retained")
		fixture.release(t, "retained-error")
		waitForProcessExit(t, pid)
		waitForHLSJobExit(t, manager, request.PlaylistPath)
		outside := filepath.Join(t.TempDir(), "outside.ts")
		if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
			t.Fatal(err)
		}
		segment := fixture.segmentPath(request, 0)
		if err := os.Remove(segment); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, segment); err != nil {
			t.Fatal(err)
		}
		file, err := manager.OpenLegacyHLS(context.Background(), "retained-error", "retained-error0.ts")
		if file != nil {
			_ = file.Close()
		}
		if err == nil || errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("retained exited selected-family open error = %v, want preserved non-not-found error", err)
		}
	})
}

func TestOpenLegacyHLSRetainsCompletedAssociatedFamily(t *testing.T) {
	fixture := newHLSFixture(t, "exit-on-go")
	manager := fixture.manager(t, clock.NewReal())
	defer closeHLSManager(t, manager)
	request := fixture.request("retained", 4)
	if err := manager.StartHLS(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	fixture.waitStarted(t, "retained")
	pid := fixture.waitPID(t, "retained")
	fixture.writePlaylist(t, request)
	fixture.writeSegment(t, request, 0, "retained segment")
	fixture.release(t, "retained")
	waitForProcessExit(t, pid)

	file, err := manager.OpenLegacyHLS(context.Background(), "retained", "retained0.ts")
	if err != nil {
		t.Fatalf("OpenLegacyHLS retained segment: %v", err)
	}
	defer closeHLSSegment(t, file)
	if got := readHLSSegment(t, file); got != "retained segment" {
		t.Fatalf("retained legacy segment = %q, want retained segment", got)
	}
}

func TestOpenLegacyHLSRejectsUnsafeLegacyTokensAndSymlinkArtifacts(t *testing.T) {
	fixture := newHLSFixture(t, "hold")
	manager := fixture.manager(t, clock.NewReal())
	defer closeHLSManager(t, manager)
	request := fixture.request("safe", 4)
	if err := manager.StartHLS(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	fixture.waitStarted(t, "safe")
	fixture.writePlaylist(t, request)
	fixture.writeSegment(t, request, 0, "safe segment")

	for _, tc := range []struct {
		name       string
		playlistID string
		artifact   string
	}{
		{name: "playlist separator", playlistID: "safe/other", artifact: "safe0.ts"},
		{name: "playlist backslash", playlistID: `safe\\other`, artifact: "safe0.ts"},
		{name: "playlist dot", playlistID: ".", artifact: "safe0.ts"},
		{name: "playlist control", playlistID: "safe\nother", artifact: "safe0.ts"},
		{name: "artifact separator", playlistID: "safe", artifact: "nested/safe0.ts"},
		{name: "artifact backslash", playlistID: "safe", artifact: `nested\\safe0.ts`},
		{name: "artifact dot", playlistID: "safe", artifact: ".."},
		{name: "artifact control", playlistID: "safe", artifact: "safe0.ts\x00"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file, err := manager.OpenLegacyHLS(context.Background(), tc.playlistID, tc.artifact)
			if file != nil {
				_ = file.Close()
			}
			if err == nil {
				t.Fatal("OpenLegacyHLS accepted an unsafe legacy token")
			}
		})
	}

	outside := filepath.Join(t.TempDir(), "outside.ts")
	if err := os.WriteFile(outside, []byte("outside bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	segment := fixture.segmentPath(request, 0)
	if err := os.Remove(segment); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, segment); err != nil {
		t.Fatal(err)
	}
	file, err := manager.OpenLegacyHLS(context.Background(), "safe", "safe0.ts")
	if file != nil {
		_ = file.Close()
	}
	if err == nil {
		t.Fatal("OpenLegacyHLS followed an artifact symlink outside its descriptor-relative root")
	}
}

func TestOpenLegacyHLSEmptyPlaylistIDRequiresOneActiveAudioFamily(t *testing.T) {
	fixture := newProgressiveFixture(t, "hold")
	manager := fixture.manager(t, clock.NewReal())
	defer closeProgressiveManager(t, manager)
	firstRequest := fixture.request(filepath.Join("audio", "stream.mp3"))
	first, err := manager.OpenProgressive(context.Background(), firstRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer closeProgressiveReader(t, first)
	fixture.waitStarted(t)

	file, err := manager.OpenLegacyHLS(context.Background(), "", "stream.mp3")
	if err != nil {
		t.Fatalf("OpenLegacyHLS with one active audio family: %v", err)
	}
	closeHLSSegment(t, file)

	secondRequest := fixture.request(filepath.Join("other", "stream.mp3"))
	second, err := manager.OpenProgressive(context.Background(), secondRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer closeProgressiveReader(t, second)
	file, err = manager.OpenLegacyHLS(context.Background(), "", "stream.mp3")
	if file != nil {
		_ = file.Close()
	}
	if err == nil {
		t.Fatal("OpenLegacyHLS selected an ambiguous empty-playlist audio artifact family")
	}
}

func TestOpenLegacyHLSPingsActivityAndRejectsShutdown(t *testing.T) {
	base := clock.NewFake(time.Date(2026, time.July, 28, 12, 0, 0, 0, time.UTC))
	fixture := newHLSFixture(t, "hold")
	manager := fixture.manager(t, base)
	request := fixture.request("activity", 4)
	if err := manager.StartHLS(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	fixture.waitStarted(t, "activity")
	pid := fixture.waitPID(t, "activity")
	fixture.writePlaylist(t, request)
	fixture.writeSegment(t, request, 0, "activity segment")

	base.Advance(59 * time.Second)
	file, err := manager.OpenLegacyHLS(context.Background(), "activity", "activity0.ts")
	if err != nil {
		t.Fatalf("OpenLegacyHLS: %v", err)
	}
	closeHLSSegment(t, file)
	base.Advance(time.Second)
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("legacy artifact open did not reset HLS activity: %v", err)
	}

	if err := manager.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	file, err = manager.OpenLegacyHLS(context.Background(), "activity", "activity0.ts")
	if file != nil {
		_ = file.Close()
	}
	if err == nil {
		t.Fatal("OpenLegacyHLS succeeded after manager shutdown")
	}
}

func TestOpenLegacyHLSBodyRetainsLeaseUntilCloseAndAdvancesDownloadPosition(t *testing.T) {
	base := clock.NewFake(time.Date(2026, time.July, 28, 12, 0, 0, 0, time.UTC))
	observed := newProgressiveObservedClock(base)
	fixture := newHLSFixture(t, "hold")
	manager := fixture.manager(t, observed)
	defer closeHLSManager(t, manager)
	request := fixture.request("legacy-lease", 4)
	if err := manager.StartHLS(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	fixture.waitStarted(t, "legacy-lease")
	pid := fixture.waitPID(t, "legacy-lease")
	fixture.writePlaylist(t, request)
	fixture.writeSegment(t, request, 0, "legacy body")
	fixture.writeSegment(t, request, 1, "successor")
	file, err := manager.OpenLegacyHLS(context.Background(), "legacy-lease", "legacy-lease0.ts")
	if err != nil {
		t.Fatalf("OpenLegacyHLS: %v", err)
	}
	if got := readHLSSegment(t, file); got != "legacy body" {
		_ = file.Close()
		t.Fatalf("legacy body = %q, want legacy body", got)
	}
	base.Advance(60 * time.Second)
	if err := syscall.Kill(pid, 0); err != nil {
		_ = file.Close()
		t.Fatalf("idle shutdown killed HLS while legacy response body remained open: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	canonical, err := manager.normalizeOutputPath(request.PlaylistPath)
	if err != nil {
		t.Fatal(err)
	}
	manager.mu.Lock()
	downloadPosition := manager.jobs[canonical].downloadPositionTicks
	manager.mu.Unlock()
	if want := int64(4 * time.Second / 100); downloadPosition != want {
		t.Fatalf("download position ticks after legacy body Close = %d, want %d", downloadPosition, want)
	}
	observed.waitForTimerCount(t, 60*time.Second, 2)
	base.Advance(60 * time.Second)
	fixture.waitQ(t, "legacy-lease")
	waitForProcessExit(t, pid)
}

func TestStartHLSOwnsSidecarWorkersUntilManagerClose(t *testing.T) {
	t.Cleanup(func() { goleak.VerifyNone(t, goleak.IgnoreCurrent()) })
	base := clock.NewFake(time.Date(2026, time.July, 28, 12, 0, 0, 0, time.UTC))
	observed := newProgressiveObservedClock(base)
	fixture := newHLSFixture(t, "hold")
	logDirectory := filepath.Join(fixture.dir, "logs")
	manager, err := NewTranscodeManager(fixture.ffmpeg, fixture.dir, TranscodeManagerOptions{
		Clock:                  observed,
		OutputPollInterval:     100 * time.Millisecond,
		ReaderRetryInterval:    50 * time.Millisecond,
		ReaderStallTimeout:     30 * time.Second,
		ProgressiveIdleTimeout: 10 * time.Second,
		KillGracePeriod:        5 * time.Second,
		LogDirectory:           logDirectory,
		MaxLogBytes:            1024,
		ThrottleThreshold:      5 * time.Second,
		SegmentRetention:       1500 * time.Millisecond,
		PKeySupported:          true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeHLSManager(t, manager) })
	t.Setenv("HLS_EMIT_PROGRESS", "1")
	t.Setenv("HLS_PROGRESS_TIME", "00:01:05.000")
	request := fixture.request("sidecars", 4)
	request.MediaSourceID = "sidecar-source"
	request.RunTimeTicks = int64(120 * time.Second / 100)
	request.StartTimeTicks = 0
	if err := manager.StartHLS(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	fixture.waitStarted(t, "sidecars")
	observed.waitForTimer(t, 5*time.Second)
	observed.waitForTimer(t, 20*time.Second)
	waitForHLSProgress(t, manager, request.PlaylistPath)
	fixture.writePlaylist(t, request)
	fixture.writeSegment(t, request, 0, "consumed segment")
	fixture.writeSegment(t, request, 1, "successor")
	segment, err := manager.WaitSegment(context.Background(), request.PlaylistPath, 0)
	if err != nil {
		t.Fatalf("WaitSegment consumed segment: %v", err)
	}
	if got := readHLSSegment(t, segment); got != "consumed segment" {
		_ = segment.Close()
		t.Fatalf("consumed segment bytes = %q, want consumed segment", got)
	}
	if err := segment.Close(); err != nil {
		t.Fatal(err)
	}
	canonical, err := manager.normalizeOutputPath(request.PlaylistPath)
	if err != nil {
		t.Fatal(err)
	}
	manager.mu.Lock()
	downloadPosition := manager.jobs[canonical].downloadPositionTicks
	manager.mu.Unlock()
	if want := int64(4 * time.Second / 100); downloadPosition != want {
		t.Fatalf("consumed HLS segment download ticks = %d, want %d", downloadPosition, want)
	}
	base.Advance(5 * time.Second)
	fixture.waitP(t, "sidecars")
	entries, err := os.ReadDir(logDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("HLS sidecar log entries = %d, want one owned log", len(entries))
	}
	log, err := os.ReadFile(filepath.Join(logDirectory, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(log), "frame=1 fps=25") || len(log) > 1024 {
		t.Fatalf("HLS sidecar log = %q (%d bytes), want bounded ffmpeg progress", log, len(log))
	}

	closed := make(chan error, 1)
	go func() { closed <- manager.Close(context.Background()) }()
	fixture.waitQ(t, "sidecars")
	if err := receiveProgressiveError(t, closed); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestHLSSidecarsParseProgressAndThrottleAfterStderrLogCap(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	base := clock.NewFake(time.Date(2026, time.July, 28, 12, 0, 0, 0, time.UTC))
	observed := newProgressiveObservedClock(base)
	fixture := newHLSFixture(t, "hold")
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
		ThrottleThreshold:      5 * time.Second,
		SegmentRetention:       1500 * time.Millisecond,
		PKeySupported:          true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer closeHLSManager(t, manager)
	t.Setenv("HLS_EMIT_OVERSIZED_PROGRESS", "1")
	request := fixture.request("oversized-stderr", 4)
	request.MediaSourceID = "oversized-stderr"
	request.RunTimeTicks = int64(120 * time.Second / 100)
	if err := manager.StartHLS(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	fixture.waitStarted(t, "oversized-stderr")
	observed.waitForTimer(t, 5*time.Second)
	waitForHLSProgressWithin(t, manager, request.PlaylistPath, 30*time.Second)
	base.Advance(5 * time.Second)
	fixture.waitP(t, "oversized-stderr")
	entries, err := os.ReadDir(logDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("capped HLS log entries = %d, want one", len(entries))
	}
	info, err := entries[0].Info()
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > 1<<20 {
		t.Fatalf("HLS stderr log size = %d, want at most 1MiB", info.Size())
	}
}

// TestStopPlaySessionUnpausesThrottledJobBeforeQuit checks that a stop
// unpauses a throttled job before it sends "q". In the "c" pause ffmpeg reads
// the next stdin line as a filter command, so a "q" without an unpause first
// does not stop it and the stop waits for the kill grace period.
func TestStopPlaySessionUnpausesThrottledJobBeforeQuit(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	base := clock.NewFake(time.Date(2026, time.July, 28, 12, 0, 0, 0, time.UTC))
	observed := newProgressiveObservedClock(base)
	fixture := newHLSFixture(t, "pause-c")
	manager, err := NewTranscodeManager(fixture.ffmpeg, fixture.dir, TranscodeManagerOptions{
		Clock:                  observed,
		OutputPollInterval:     100 * time.Millisecond,
		ReaderRetryInterval:    50 * time.Millisecond,
		ReaderStallTimeout:     30 * time.Second,
		ProgressiveIdleTimeout: 10 * time.Second,
		KillGracePeriod:        5 * time.Second,
		LogDirectory:           filepath.Join(fixture.dir, "logs"),
		ThrottleThreshold:      time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer closeHLSManager(t, manager)
	t.Setenv("HLS_EMIT_PROGRESS", "1")
	t.Setenv("HLS_PROGRESS_TIME", "00:01:01.000")
	request := fixture.request("pause-c", 4)
	if err := manager.StartHLS(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	fixture.waitStarted(t, "pause-c")
	observed.waitForTimer(t, 5*time.Second)
	waitForHLSProgressWithin(t, manager, request.PlaylistPath, 30*time.Second)
	base.Advance(5 * time.Second)
	waitForFile(t, fixture.marker("c", "pause-c"))

	stopped := make(chan error, 1)
	go func() {
		stopped <- manager.StopPlaySession(context.Background(), request.PlaySessionID, request.DeviceID)
	}()
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatalf("StopPlaySession: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("StopPlaySession waited for the kill grace period: the paused job did not read q")
	}
	fixture.waitQ(t, "pause-c")
	commands, err := os.ReadFile(fixture.marker("filter-commands", "pause-c"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(commands), "\n"; got != want {
		t.Fatalf("filter command lines = %q, want one empty unpause line %q", got, want)
	}
}

func TestNaturalHLSLeaderExitReapsStderrChildAndServesFinalWaiter(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	fixture := newHLSFixture(t, "exit-with-stderr-child")
	logDirectory := filepath.Join(fixture.dir, "logs")
	manager, err := NewTranscodeManager(fixture.ffmpeg, fixture.dir, TranscodeManagerOptions{
		Clock:                  clock.NewReal(),
		OutputPollInterval:     100 * time.Millisecond,
		ReaderRetryInterval:    50 * time.Millisecond,
		ReaderStallTimeout:     30 * time.Second,
		ProgressiveIdleTimeout: 10 * time.Second,
		KillGracePeriod:        5 * time.Second,
		LogDirectory:           logDirectory,
		MaxLogBytes:            64 << 10,
		ThrottleThreshold:      5 * time.Second,
		SegmentRetention:       1500 * time.Millisecond,
		PKeySupported:          true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer closeHLSManager(t, manager)
	t.Setenv("HLS_EMIT_PROGRESS", "1")
	request := fixture.request("natural-child", 4)
	request.MediaSourceID = "natural-child"
	request.RunTimeTicks = int64(120 * time.Second / 100)
	if err := manager.StartHLS(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	fixture.waitStarted(t, "natural-child")
	parent, child := fixture.waitPID(t, "natural-child"), fixture.waitChildPID(t, "natural-child")
	fixture.writePlaylist(t, request)
	fixture.writeSegment(t, request, 0, "final through child stderr")
	results := make(chan hlsWaitResult, 1)
	go func() {
		file, err := manager.WaitSegment(context.Background(), request.PlaylistPath, 0)
		results <- hlsWaitResult{file: file, err: err}
	}()
	file := receiveHLSFile(t, results)
	defer closeHLSSegment(t, file)
	if got := readHLSSegment(t, file); got != "final through child stderr" {
		t.Fatalf("final segment after natural leader exit = %q", got)
	}
	waitForProcessExit(t, parent)
	waitForProcessExit(t, child)
	waitForHLSProgress(t, manager, request.PlaylistPath)
	entries, err := os.ReadDir(logDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("natural-exit HLS log entries = %d, want one", len(entries))
	}
	info, err := entries[0].Info()
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > 64<<10 {
		t.Fatalf("natural-exit HLS log size = %d, want bounded at 64KiB", info.Size())
	}
}

func TestNaturalHLSLeaderExitDrainsFinalStderrBeforeClosingSidecars(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	fixture := newHLSFixture(t, "exit-with-final-stderr-child")
	logDirectory := filepath.Join(fixture.dir, "logs")
	manager, err := NewTranscodeManager(fixture.ffmpeg, fixture.dir, TranscodeManagerOptions{
		Clock:                  clock.NewReal(),
		OutputPollInterval:     100 * time.Millisecond,
		ReaderRetryInterval:    50 * time.Millisecond,
		ReaderStallTimeout:     30 * time.Second,
		ProgressiveIdleTimeout: 10 * time.Second,
		KillGracePeriod:        5 * time.Second,
		LogDirectory:           logDirectory,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer closeHLSManager(t, manager)

	request := fixture.request("final-stderr-child", 4)
	request.MediaSourceID = "final-stderr-child"
	request.RunTimeTicks = int64(120 * time.Second / 100)
	if err := manager.StartHLS(t.Context(), request); err != nil {
		t.Fatalf("StartHLS: %v", err)
	}
	fixture.waitStarted(t, "final-stderr-child")
	waitForFile(t, fixture.marker("final-stderr", "final-stderr-child"))
	waitForHLSJobExit(t, manager, request.PlaylistPath)

	canonical, err := manager.normalizeOutputPath(request.PlaylistPath)
	if err != nil {
		t.Fatal(err)
	}
	manager.mu.Lock()
	job := manager.jobs[canonical]
	if job == nil {
		manager.mu.Unlock()
		t.Fatal("retained HLS job disappeared after successful leader exit")
	}
	position, bytesTranscoded := job.positionTicks, job.bytesTranscoded
	hasBytes := job.hasBytesTranscoded
	manager.mu.Unlock()
	if want := int64((42*time.Second + 500*time.Millisecond) / 100); position != want {
		t.Fatalf("final sidecar position ticks = %d, want %d", position, want)
	}
	if !hasBytes || bytesTranscoded != 17*1024 {
		t.Fatalf("final sidecar bytes = %d (reported=%t), want %d reported", bytesTranscoded, hasBytes, 17*1024)
	}
	entries, err := os.ReadDir(logDirectory)
	if err != nil {
		t.Fatalf("read final stderr diagnostics: %v", err)
	}
	if got, want := len(entries), 1; got != want {
		t.Fatalf("final stderr diagnostic logs = %d, want %d", got, want)
	}
	diagnostics, err := os.ReadFile(filepath.Join(logDirectory, entries[0].Name()))
	if err != nil {
		t.Fatalf("read final stderr diagnostic: %v", err)
	}
	if !strings.Contains(string(diagnostics), "frame=9 fps=27.5 size=17kB time=00:00:42.500") {
		t.Fatalf("final stderr progress was not drained into diagnostics: %q", diagnostics)
	}
}

func TestStopPlaySessionSurfacesClosedWaitCleanupFailureAndRetries(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	fixture := newHLSFixture(t, "exit-with-stderr-child")
	manager := fixture.manager(t, clock.NewReal())
	defer closeHLSManager(t, manager)
	defer func() { manager.reapProcessGroup = nil }()
	sentinel := errors.New("injected HLS reap failure")
	manager.reapProcessGroup = func(int) error { return sentinel }
	request := fixture.request("retry-reap", 4)
	if err := manager.StartHLS(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	fixture.waitStarted(t, "retry-reap")
	child := fixture.waitChildPID(t, "retry-reap")
	waitForHLSJobExit(t, manager, request.PlaylistPath)
	if err := manager.StopPlaySession(context.Background(), request.PlaySessionID, request.DeviceID); !errors.Is(err, sentinel) {
		t.Fatalf("StopPlaySession after closed wait error = %v, want injected reap failure", err)
	}
	manager.mu.Lock()
	jobs, sessions := len(manager.jobs), len(manager.hlsSessions)
	manager.mu.Unlock()
	if jobs != 1 || sessions != 1 {
		t.Fatalf("failed StopPlaySession did not retain retryable state: jobs=%d sessions=%d", jobs, sessions)
	}
	manager.reapProcessGroup = nil
	if err := manager.StopPlaySession(context.Background(), request.PlaySessionID, request.DeviceID); err != nil {
		t.Fatalf("StopPlaySession retry after reaper recovery: %v", err)
	}
	waitForProcessExit(t, child)
	manager.mu.Lock()
	jobs, sessions, locks := len(manager.jobs), len(manager.hlsSessions), len(manager.hlsLocks)
	manager.mu.Unlock()
	if jobs != 0 || sessions != 0 || locks != 0 {
		t.Fatalf("successful StopPlaySession retry left state: jobs=%d sessions=%d locks=%d", jobs, sessions, locks)
	}
}

func TestTranscodeManagerCloseRetainsExitedReapPendingHLSFamilyUntilRetry(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	fixture := newHLSFixture(t, "exit-with-stderr-child")
	manager := fixture.manager(t, clock.NewReal())
	defer func() {
		manager.reapProcessGroup = nil
		_ = manager.Close(context.Background())
	}()
	sentinel := errors.New("injected HLS Close reap failure")
	manager.reapProcessGroup = func(int) error { return sentinel }
	request := fixture.request("close-reap-pending", 4)
	if err := manager.StartHLS(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	fixture.waitStarted(t, "close-reap-pending")
	child := fixture.waitChildPID(t, "close-reap-pending")
	fixture.writePlaylist(t, request)
	fixture.writeSegment(t, request, 0, "retained pending segment")
	fixture.writeSegment(t, request, 1, "successor")
	waitForHLSJobExit(t, manager, request.PlaylistPath)
	first := make(chan error, 1)
	go func() { first <- manager.Close(context.Background()) }()
	if err := receiveProgressiveError(t, first); !errors.Is(err, sentinel) {
		t.Fatalf("first Close after exited reap failure = %v, want injected reap failure", err)
	}
	manager.mu.Lock()
	jobs, sessions, workers := len(manager.jobs), len(manager.hlsSessions), manager.workerCount
	manager.mu.Unlock()
	if jobs != 1 || sessions != 1 || workers != 0 {
		t.Fatalf("first Close did not retain pending family or stop sidecars: jobs=%d sessions=%d workers=%d", jobs, sessions, workers)
	}
	for _, path := range []string{request.PlaylistPath, fixture.segmentPath(request, 0), fixture.segmentPath(request, 1)} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("first Close removed reap-pending HLS artifact %q: %v", path, err)
		}
	}
	manager.reapProcessGroup = nil
	if err := manager.Close(context.Background()); err != nil {
		t.Fatalf("second Close after reaper recovery: %v", err)
	}
	waitForProcessExit(t, child)
	for _, path := range []string{request.PlaylistPath, fixture.segmentPath(request, 0), fixture.segmentPath(request, 1)} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("second Close left HLS artifact %q: %v", path, err)
		}
	}
	manager.mu.Lock()
	jobs, sessions, locks, workers := len(manager.jobs), len(manager.hlsSessions), len(manager.hlsLocks), manager.workerCount
	manager.mu.Unlock()
	if jobs != 0 || sessions != 0 || locks != 0 || workers != 0 {
		t.Fatalf("second Close left HLS state: jobs=%d sessions=%d locks=%d workers=%d", jobs, sessions, locks, workers)
	}
}

func TestStopPlaySessionRetriesOnlyRequestedPendingFamily(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	fixture := newHLSFixture(t, "exit-with-stderr-child")
	manager := fixture.manager(t, clock.NewReal())
	defer closeHLSManager(t, manager)
	defer func() { manager.reapProcessGroup = nil }()
	sentinel := errors.New("unrelated HLS reap failure")
	manager.reapProcessGroup = func(int) error { return sentinel }
	target := fixture.request("target-pending-stop", 4)
	other := fixture.request("other-pending-stop", 4)
	other.PlaySessionID = "other-play-session"
	other.DeviceID = "other-device"
	for _, request := range []HLSRequest{target, other} {
		if err := manager.StartHLS(context.Background(), request); err != nil {
			t.Fatal(err)
		}
	}
	fixture.waitStarted(t, "target-pending-stop")
	targetPID, targetChild := fixture.waitPID(t, "target-pending-stop"), fixture.waitChildPID(t, "target-pending-stop")
	fixture.waitStarted(t, "other-pending-stop")
	otherChild := fixture.waitChildPID(t, "other-pending-stop")
	waitForHLSJobExit(t, manager, target.PlaylistPath)
	waitForHLSJobExit(t, manager, other.PlaylistPath)
	manager.reapProcessGroup = func(pid int) error {
		if pid == targetPID {
			return reapProcessGroup(pid)
		}
		return sentinel
	}
	if err := manager.StopPlaySession(context.Background(), target.PlaySessionID, target.DeviceID); err != nil {
		t.Fatalf("StopPlaySession target pending family: %v", err)
	}
	waitForProcessExit(t, targetChild)
	if err := syscall.Kill(otherChild, 0); err != nil {
		t.Fatalf("target StopPlaySession reaped unrelated pending child: %v", err)
	}
	manager.mu.Lock()
	jobs, sessions := len(manager.jobs), len(manager.hlsSessions)
	manager.mu.Unlock()
	if jobs != 1 || sessions != 1 {
		t.Fatalf("target StopPlaySession left wrong pending ownership: jobs=%d sessions=%d", jobs, sessions)
	}
	if err := manager.StopPlaySession(context.Background(), other.PlaySessionID, other.DeviceID); !errors.Is(err, sentinel) {
		t.Fatalf("StopPlaySession unrelated pending family = %v, want reap failure", err)
	}
	manager.reapProcessGroup = nil
	if err := manager.StopPlaySession(context.Background(), other.PlaySessionID, other.DeviceID); err != nil {
		t.Fatalf("StopPlaySession unrelated retry cleanup: %v", err)
	}
	waitForProcessExit(t, otherChild)
}

func TestRestartHLSAtPreservesReapPendingExitedFamilyUntilRetryCleanup(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	fixture := newHLSFixture(t, "exit-with-stderr-child")
	manager := fixture.manager(t, clock.NewReal())
	defer closeHLSManager(t, manager)
	defer func() { manager.reapProcessGroup = nil }()
	sentinel := errors.New("injected HLS reap failure")
	manager.reapProcessGroup = func(int) error { return sentinel }
	request := fixture.request("pending-restart", 4)
	if err := manager.StartHLS(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	fixture.waitStarted(t, "pending-restart")
	child := fixture.waitChildPID(t, "pending-restart")
	fixture.writePlaylist(t, request)
	fixture.writeSegment(t, request, 0, "retained pending segment")
	fixture.writeSegment(t, request, 1, "successor")
	waitForHLSJobExit(t, manager, request.PlaylistPath)
	if err := manager.RestartHLSAt(context.Background(), request, 9); !errors.Is(err, sentinel) {
		t.Fatalf("RestartHLSAt against reap-pending family = %v, want injected reap failure", err)
	}
	if err := manager.RestartHLSAt(context.Background(), request, 0); !errors.Is(err, sentinel) {
		t.Fatalf("RestartHLSAt cached reap-pending segment = %v, want injected reap failure", err)
	}
	manager.reapProcessGroup = nil
	if got := hlsStartCount(t, fixture, "pending-restart"); got != 1 {
		t.Fatalf("RestartHLSAt overlapped reap-pending process group with %d starts, want original only", got)
	}
	for _, path := range []string{request.PlaylistPath, fixture.segmentPath(request, 0), fixture.segmentPath(request, 1)} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("RestartHLSAt removed reap-pending family artifact %q: %v", path, err)
		}
	}
	if err := syscall.Kill(child, 0); err != nil {
		t.Fatalf("RestartHLSAt reaped surviving child before retry cleanup: %v", err)
	}
	manager.mu.Lock()
	jobs, sessions := len(manager.jobs), len(manager.hlsSessions)
	manager.mu.Unlock()
	if jobs != 1 || sessions != 1 {
		t.Fatalf("RestartHLSAt unindexed reap-pending family: jobs=%d sessions=%d", jobs, sessions)
	}
	manager.reapProcessGroup = nil
	if err := manager.StopPlaySession(context.Background(), request.PlaySessionID, request.DeviceID); err != nil {
		t.Fatalf("StopPlaySession retry cleanup: %v", err)
	}
	waitForProcessExit(t, child)
	for _, path := range []string{request.PlaylistPath, fixture.segmentPath(request, 0), fixture.segmentPath(request, 1)} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("retry cleanup left HLS family artifact %q: %v", path, err)
		}
	}
}

func TestTranscodeManagerCloseReapsAndCleansHLSPlaylistFamily(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	base := clock.NewFake(time.Date(2026, time.July, 28, 12, 0, 0, 0, time.UTC))
	observed := newProgressiveObservedClock(base)
	fixture := newHLSFixture(t, "ignore-q-with-child")
	manager := fixture.manager(t, observed)
	request := fixture.request("cleanup", 4)
	if err := manager.StartHLS(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	fixture.waitStarted(t, "cleanup")
	parent, child := fixture.waitPID(t, "cleanup"), fixture.waitChildPID(t, "cleanup")
	fixture.writePlaylist(t, request)
	fixture.writeSegment(t, request, 0, "old")
	fixture.writeSegment(t, request, 1, "next")

	closed := make(chan error, 1)
	go func() { closed <- manager.Close(context.Background()) }()
	fixture.waitQ(t, "cleanup")
	observed.waitForTimer(t, 5*time.Second)
	base.Advance(5 * time.Second)
	if err := receiveProgressiveError(t, closed); err != nil {
		t.Fatalf("Close: %v", err)
	}
	waitForProcessExit(t, parent)
	waitForProcessExit(t, child)
	for _, path := range []string{request.PlaylistPath, fixture.segmentPath(request, 0), fixture.segmentPath(request, 1)} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("Close left HLS family member %q behind: %v", path, err)
		}
	}
}

func TestHLSSidecarOversizedStderrTokenDrainsAndShutdownReaps(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	fixture := newHLSFixture(t, "hold")
	t.Setenv("HLS_EMIT_OVERSIZED_TOKEN", "1")
	manager := fixture.manager(t, clock.NewReal())
	closed := false
	t.Cleanup(func() {
		if closed {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), fixtureTimeout)
		defer cancel()
		if err := manager.Close(ctx); err != nil {
			t.Errorf("manager cleanup Close: %v", err)
		}
	})
	request := fixture.request("oversized-stderr-token", 4)

	if err := manager.StartHLS(t.Context(), request); err != nil {
		t.Fatalf("StartHLS: %v", err)
	}
	fixture.waitStarted(t, "oversized-stderr-token")
	pid := fixture.waitPID(t, "oversized-stderr-token")
	// The fixture creates this marker only after it has written one unterminated
	// stderr token larger than the logger scanner limit. Reaching it proves the
	// pump continued draining after the logger exited.
	waitForFile(t, fixture.marker("oversized-token", "oversized-stderr-token"))

	ctx, cancel := context.WithTimeout(context.Background(), fixtureTimeout)
	defer cancel()
	if err := manager.Close(ctx); err != nil {
		t.Fatalf("Close after oversized stderr token: %v", err)
	}
	closed = true
	waitForProcessExit(t, pid)
	assertNoSidecarWorkers(t, manager)
}

func TestWaitPlaylistRejectsOversizedLiveFamilyAndRetiresIt(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	fixture := newHLSFixture(t, "hold")
	manager := fixture.manager(t, clock.NewReal())
	closed := false
	t.Cleanup(func() {
		if closed {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), fixtureTimeout)
		defer cancel()
		if err := manager.Close(ctx); err != nil {
			t.Errorf("manager cleanup Close: %v", err)
		}
	})
	request := fixture.request("oversized-live-playlist", 4)
	request.Live = true
	request.MinSegments = 0
	if err := manager.StartHLS(t.Context(), request); err != nil {
		t.Fatalf("StartHLS: %v", err)
	}
	fixture.waitStarted(t, "oversized-live-playlist")
	pid := fixture.waitPID(t, "oversized-live-playlist")
	oversized := "#EXTM3U\n" + strings.Repeat("#\n", (maxHLSPlaylistBytes-len("#EXTM3U\n"))/2+1)
	if len(oversized) <= maxHLSPlaylistBytes {
		t.Fatalf("oversized fixture length = %d, want more than %d", len(oversized), maxHLSPlaylistBytes)
	}
	fixture.writePlaylistText(t, request, oversized)

	ctx, cancel := context.WithTimeout(context.Background(), fixtureTimeout)
	_, err := manager.WaitPlaylist(ctx, request.PlaylistPath, 0)
	cancel()
	if !errors.Is(err, ErrHLSPlaylistTooLarge) {
		t.Fatalf("WaitPlaylist oversized error = %v, want %v", err, ErrHLSPlaylistTooLarge)
	}

	waitForProcessExit(t, pid)
	waitForHLSFamilyRetirement(t, manager, request.PlaylistPath)
	waitForProgressiveRemoval(t, request.PlaylistPath)
	ctx, cancel = context.WithTimeout(context.Background(), fixtureTimeout)
	_, err = manager.WaitPlaylist(ctx, request.PlaylistPath, 0)
	cancel()
	if err == nil {
		t.Fatal("repeated WaitPlaylist succeeded after oversized family retirement")
	}
	if got := hlsStartCount(t, fixture, "oversized-live-playlist"); got != 1 {
		t.Fatalf("oversized live family starts = %d, want one", got)
	}

	ctx, cancel = context.WithTimeout(context.Background(), fixtureTimeout)
	defer cancel()
	if err := manager.Close(ctx); err != nil {
		t.Fatalf("Close after oversized live playlist: %v", err)
	}
	closed = true
	assertNoSidecarWorkers(t, manager)
}

func TestWaitPlaylistAcceptsExactEightMiBLiveManifest(t *testing.T) {
	fixture := newHLSFixture(t, "hold")
	manager := fixture.manager(t, clock.NewReal())
	closed := false
	t.Cleanup(func() {
		if closed {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), fixtureTimeout)
		defer cancel()
		if err := manager.Close(ctx); err != nil {
			t.Errorf("manager cleanup Close: %v", err)
		}
	})
	request := fixture.request("exact-live-playlist", 4)
	request.Live = true
	request.MinSegments = 0
	if err := manager.StartHLS(t.Context(), request); err != nil {
		t.Fatalf("StartHLS: %v", err)
	}
	fixture.waitStarted(t, "exact-live-playlist")
	want := "#EXTM3U\n" + strings.Repeat("#\n", (maxHLSPlaylistBytes-len("#EXTM3U\n"))/2)
	if len(want) != maxHLSPlaylistBytes {
		t.Fatalf("exact playlist length = %d, want %d", len(want), maxHLSPlaylistBytes)
	}
	fixture.writePlaylistText(t, request, want)

	ctx, cancel := context.WithTimeout(context.Background(), fixtureTimeout)
	playlist, err := manager.WaitPlaylist(ctx, request.PlaylistPath, 0)
	cancel()
	if err != nil {
		t.Fatalf("WaitPlaylist at exact cap: %v", err)
	}
	if playlist != want {
		t.Fatalf("exact-cap playlist length = %d, want %d", len(playlist), len(want))
	}

	ctx, cancel = context.WithTimeout(context.Background(), fixtureTimeout)
	defer cancel()
	if err := manager.Close(ctx); err != nil {
		t.Fatalf("Close after exact-cap playlist: %v", err)
	}
	closed = true
}

func waitForHLSFamilyRetirement(t *testing.T, manager *TranscodeManager, playlist string) {
	t.Helper()
	canonicalPlaylist, err := manager.normalizeOutputPath(playlist)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.NewTimer(fixtureTimeout)
	defer deadline.Stop()
	for {
		manager.mu.Lock()
		retired := manager.jobs[canonicalPlaylist] == nil
		manager.mu.Unlock()
		if retired {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("timed out waiting for oversized HLS family %q to retire", playlist)
		case <-time.After(time.Millisecond):
		}
	}
}

type hlsFixture struct {
	ffmpeg, dir, root string
}

func newHLSFixture(t *testing.T, mode string) hlsFixture {
	t.Helper()
	root := t.TempDir()
	fixture := hlsFixture{root: root, dir: filepath.Join(root, "transcodes")}
	t.Setenv("HLS_FIXTURE_ROOT", root)
	t.Setenv("HLS_FIXTURE_MODE", mode)
	fixture.ffmpeg = hlsFakeFFmpeg(t)
	return fixture
}

func (f hlsFixture) manager(t *testing.T, testClock clock.Clock) *TranscodeManager {
	t.Helper()
	manager, err := NewTranscodeManager(f.ffmpeg, f.dir, TranscodeManagerOptions{
		Clock:                  testClock,
		OutputPollInterval:     100 * time.Millisecond,
		ReaderRetryInterval:    50 * time.Millisecond,
		ReaderStallTimeout:     30 * time.Second,
		ProgressiveIdleTimeout: 10 * time.Second,
		KillGracePeriod:        5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func (f hlsFixture) request(label string, segmentLengthSeconds int) HLSRequest {
	return f.requestWithExtension(label, segmentLengthSeconds, ".ts")
}

func (f hlsFixture) requestWithExtension(label string, segmentLengthSeconds int, extension string) HLSRequest {
	playlist := filepath.Join(f.dir, label+".m3u8")
	return HLSRequest{
		PlaylistPath:         playlist,
		SegmentPattern:       filepath.Join(f.dir, label+"%d"+extension),
		Args:                 []string{"--fixture-label=" + label},
		PlaySessionID:        "session-" + label,
		DeviceID:             "device-" + label,
		SegmentLengthSeconds: segmentLengthSeconds,
		RunTimeTicks:         600 * 10_000_000,
	}
}

func (f hlsFixture) marker(kind, label string) string {
	return filepath.Join(f.root, kind+"."+label)
}

func (f hlsFixture) waitStarted(t *testing.T, label string) {
	t.Helper()
	waitForFile(t, f.marker("started", label))
}

func (f hlsFixture) waitQ(t *testing.T, label string) {
	t.Helper()
	waitForFile(t, f.marker("q", label))
}

func (f hlsFixture) waitP(t *testing.T, label string) {
	t.Helper()
	waitForFile(t, f.marker("p", label))
}

func (f hlsFixture) waitPID(t *testing.T, label string) int {
	t.Helper()
	return waitForPID(t, f.marker("pid", label))
}

func (f hlsFixture) waitChildPID(t *testing.T, label string) int {
	t.Helper()
	return waitForPID(t, f.marker("child", label))
}

func (f hlsFixture) release(t *testing.T, label string) {
	t.Helper()
	if err := os.WriteFile(f.marker("go", label), nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

func (f hlsFixture) writePlaylist(t *testing.T, request HLSRequest) {
	f.writePlaylistText(t, request, "#EXTM3U\n")
}

func (f hlsFixture) writePlaylistText(t *testing.T, request HLSRequest, text string) {
	t.Helper()
	if err := os.WriteFile(request.PlaylistPath, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (f hlsFixture) segmentPath(request HLSRequest, index int) string {
	return fmt.Sprintf(request.SegmentPattern, index)
}

func (f hlsFixture) writeSegment(t *testing.T, request HLSRequest, index int, contents string) {
	t.Helper()
	if err := os.WriteFile(f.segmentPath(request, index), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func hlsStartCount(t *testing.T, fixture hlsFixture, label string) int {
	t.Helper()
	return len(readLinesIfPresent(t, fixture.marker("starts", label)))
}

func hlsFakeFFmpeg(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ffmpeg")
	body := `#!/bin/sh
set -eu
label=
for argument do
  case "$argument" in --fixture-label=*) label=${argument#--fixture-label=} ;; esac
done
[ -n "$label" ]
root=$HLS_FIXTURE_ROOT
printf '%s\n' "$@" > "$root/argv.$label"
printf '%s\n' "$$" >> "$root/starts.$label"
printf '%s\n' "$$" > "$root/pid.$label"
: > "$root/started.$label"
if [ "${HLS_COPY_FD4-}" = 1 ]; then
  fd4tmp="$root/fd4.$label.tmp"
  if cat <&4 > "$fd4tmp" 2>/dev/null; then
    mv "$fd4tmp" "$root/fd4.$label"
  else
    rm -f "$fd4tmp"
    : > "$root/fd4-missing.$label"
  fi
fi
if [ "${HLS_EMIT_PROGRESS-}" = 1 ]; then
  printf 'frame=1 fps=25 size=1kB time=%s bitrate=8.0kbits/s\n' "${HLS_PROGRESS_TIME-00:00:01.000}" >&2
fi
if [ "${HLS_EMIT_OVERSIZED_PROGRESS-}" = 1 ]; then
  count=0
  while [ "$count" -lt 1100 ]; do printf '%1024s\n' x >&2; count=$((count + 1)); done
  printf 'frame=2 fps=25 size=2kB time=00:01:01.000 bitrate=8.0kbits/s\n' >&2
fi
if [ "${HLS_EMIT_OVERSIZED_TOKEN-}" = 1 ]; then
  count=0
  while [ "$count" -lt 1100 ]; do printf '%1024d' 0 >&2; count=$((count + 1)); done
  printf '\n' >&2
  : > "$root/oversized-token.$label"
fi
if [ -n "${HLS_AUTO_SEGMENT_WAIT-}" ]; then
  while [ ! -e "$HLS_AUTO_SEGMENT_WAIT" ]; do :; done
fi
if [ -n "${HLS_AUTO_SEGMENT-}" ]; then
  printf requested > "$HLS_AUTO_SEGMENT"
fi
case "$HLS_FIXTURE_MODE" in

hold)
  while :; do
    command=$(dd bs=1 count=1 2>/dev/null || true)
    if [ "$command" = q ]; then : > "$root/q.$label"; exit 0; fi
    if [ "$command" = p ]; then : > "$root/p.$label"; fi
  done
  ;;
pause-c)
  while :; do
    command=$(dd bs=1 count=1 2>/dev/null || true)
    if [ "$command" = q ]; then : > "$root/q.$label"; exit 0; fi
    if [ "$command" = c ]; then
      : > "$root/c.$label"
      IFS= read -r line || true
      printf '%s\n' "$line" >> "$root/filter-commands.$label"
    fi
  done
  ;;
exit-on-go)
  while [ ! -e "$root/go.$label" ]; do :; done
  exit 0
  ;;
exit-with-stderr-child)
  sh -c 'trap "" TERM; printf "%s\n" "$$" > "$HLS_FIXTURE_ROOT/child.'"$label"'"; while :; do printf x >&2; done' &
  while [ ! -e "$root/child.$label" ]; do :; done
  exit 0
  ;;
exit-with-final-stderr-child)
  sh -c 'trap "" TERM; printf "%s\n" "$$" > "$HLS_FIXTURE_ROOT/child.$1"; printf "frame=9 fps=27.5 size=17kB time=00:00:42.500 bitrate=64.0kbits/s\n" >&2; : > "$HLS_FIXTURE_ROOT/final-stderr.$1"; while :; do :; done' sh "$label" &
  while [ ! -e "$root/final-stderr.$label" ]; do :; done
  exit 0
  ;;

ignore-q-with-child)
  trap '' TERM
  sh -c 'trap "" TERM; printf "%s\n" "$$" > "$HLS_FIXTURE_ROOT/child.'"$label"'"; while :; do :; done' &
  while IFS= read -r command; do : > "$root/q.$label"; done
  ;;
*) exit 97 ;;
esac
`
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

type hlsWaitResult struct {
	file io.ReadCloser
	err  error
}

type hlsPlaylistResult struct {
	playlist string
	err      error
}

func receiveHLSPlaylist(t *testing.T, results <-chan hlsPlaylistResult) hlsPlaylistResult {
	t.Helper()
	deadline := time.NewTimer(fixtureTimeout)
	defer deadline.Stop()
	select {
	case result := <-results:
		return result
	case <-deadline.C:
		t.Fatal("timed out waiting for HLS playlist request")
		return hlsPlaylistResult{}
	}
}

func assertHLSPlaylistPending(t *testing.T, results <-chan hlsPlaylistResult) {
	t.Helper()
	select {
	case result := <-results:
		t.Fatalf("WaitPlaylist completed before its readiness condition: %v", result.err)
	default:
	}
}

func receiveHLSWait(t *testing.T, results <-chan hlsWaitResult) hlsWaitResult {
	t.Helper()
	deadline := time.NewTimer(fixtureTimeout)
	defer deadline.Stop()
	select {
	case result := <-results:
		return result
	case <-deadline.C:
		t.Fatal("timed out waiting for HLS segment request")
		return hlsWaitResult{}
	}
}

func receiveHLSFile(t *testing.T, results <-chan hlsWaitResult) io.ReadCloser {
	t.Helper()
	result := receiveHLSWait(t, results)
	if result.err != nil {
		t.Fatal(result.err)
	}
	if result.file == nil {
		t.Fatal("WaitSegment returned nil file without error")
	}
	return result.file
}

func assertHLSPending(t *testing.T, results <-chan hlsWaitResult) {
	t.Helper()
	select {
	case result := <-results:
		if result.file != nil {
			_ = result.file.Close()
		}
		t.Fatalf("WaitSegment completed before its readiness condition: %v", result.err)
	default:
	}
}

func readHLSSegment(t *testing.T, file io.Reader) string {
	t.Helper()
	data, err := io.ReadAll(file)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// makeFMP4Initialization builds a minimal fMP4 initialization segment from the
// given boxes. Each box is a 4-byte big-endian size, a 4-byte type, and the
// payload that fills the remaining size.
func makeFMP4Initialization(t *testing.T, firstSize int, firstType string, firstPayload []byte, secondSize int, secondType string) []byte {
	t.Helper()
	if firstSize != len(firstPayload)+8 {
		t.Fatalf("ftyp box size %d does not match payload length %d plus 8", firstSize, len(firstPayload))
	}
	if secondSize != 8 {
		t.Fatalf("moov box size %d must equal the 8-byte header", secondSize)
	}
	var buf bytes.Buffer
	writeFMP4Box(t, &buf, firstSize, firstType, firstPayload)
	writeFMP4Box(t, &buf, secondSize, secondType, nil)
	return buf.Bytes()
}

func writeFMP4Box(t *testing.T, buf *bytes.Buffer, size int, typ string, payload []byte) {
	t.Helper()
	if len(typ) != 4 {
		t.Fatalf("fMP4 box type %q must be four bytes", typ)
	}
	if err := binary.Write(buf, binary.BigEndian, uint32(size)); err != nil {
		t.Fatal(err)
	}
	if _, err := buf.WriteString(typ); err != nil {
		t.Fatal(err)
	}
	if _, err := buf.Write(payload); err != nil {
		t.Fatal(err)
	}
}

func closeHLSSegment(t *testing.T, file io.Closer) {
	t.Helper()
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func closeHLSManager(t *testing.T, manager *TranscodeManager) {
	t.Helper()
	if err := manager.Close(context.Background()); err != nil {
		t.Fatalf("manager cleanup: %v", err)
	}
}

func waitForHLSStarts(t *testing.T, fixture hlsFixture, label string, want int) []string {
	t.Helper()
	return waitForProgressiveStartRecords(t, fixture.marker("starts", label), want)
}

func assertHLSRestartPending(t *testing.T, results <-chan error) {
	t.Helper()
	select {
	case err := <-results:
		t.Fatalf("RestartHLSAt completed before requested progress: %v", err)
	default:
	}
}

func waitForHLSJobExit(t *testing.T, manager *TranscodeManager, playlist string) {
	t.Helper()
	canonicalPlaylist, err := manager.normalizeOutputPath(playlist)
	if err != nil {
		t.Fatal(err)
	}
	manager.mu.Lock()
	job := manager.jobs[canonicalPlaylist]
	manager.mu.Unlock()
	if job == nil {
		t.Fatalf("HLS job for %q disappeared before retained-cache assertion", playlist)
	}
	select {
	case <-job.waitDone:
	case <-time.After(fixtureTimeout):
		t.Fatalf("timed out waiting for HLS job %q to record process exit", playlist)
	}
}

func waitForHLSProgress(t *testing.T, manager *TranscodeManager, playlist string) {
	t.Helper()
	waitForHLSProgressWithin(t, manager, playlist, fixtureTimeout)
}

func waitForHLSProgressWithin(t *testing.T, manager *TranscodeManager, playlist string, timeout time.Duration) {
	t.Helper()
	canonicalPlaylist, err := manager.normalizeOutputPath(playlist)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		manager.mu.Lock()
		job := manager.jobs[canonicalPlaylist]
		progressed := job != nil && job.positionTicks > 0
		manager.mu.Unlock()
		if progressed {
			return
		}
		select {
		case <-deadline.C:
			t.Fatal("timed out waiting for HLS logger progress to update manager state")
		case <-time.After(time.Millisecond):
		}
	}
}

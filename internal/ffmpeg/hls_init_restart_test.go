// SPDX-License-Identifier: GPL-2.0-only

package ffmpeg

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"testing"

	"github.com/deadfin-org/deadfin/internal/clock"
)

// TestWaitSegmentServesFMP4InitializationAfterSeekRestart follows the
// Chromium native HLS player across a seek: it fetches the initialization
// segment again and then the target segment. The replacement job starts at
// -start_number 33 and never writes segment 0, so segment 0 cannot be the
// readiness signal for the initialization segment.
func TestWaitSegmentServesFMP4InitializationAfterSeekRestart(t *testing.T) {
	fixture := newHLSFixture(t, "hold")
	manager := fixture.manager(t, clock.NewReal())
	defer closeHLSManager(t, manager)

	requestA := fixture.requestWithExtension("seek-init-a", 4, ".mp4")
	if err := manager.StartHLS(context.Background(), requestA); err != nil {
		t.Fatal(err)
	}
	fixture.waitStarted(t, "seek-init-a")
	fixture.writePlaylist(t, requestA)
	fixture.writeSegment(t, requestA, -1, "A initialization")
	fixture.writeSegment(t, requestA, 0, "A segment 0")
	fixture.writeSegment(t, requestA, 1, "A segment 1")

	// The seek: A stops and B starts at segment 33 in the same family.
	requestB := requestA
	requestB.Args = []string{"--fixture-label=seek-init-b"}
	restarted := make(chan error, 1)
	go func() { restarted <- manager.RestartHLSAt(context.Background(), requestB, 33) }()
	fixture.waitStarted(t, "seek-init-b")
	fixture.writePlaylist(t, requestB)
	fixture.writeSegment(t, requestB, -1, "B initialization")

	// The player asks for the initialization segment again, then for 33.
	initResults := make(chan hlsWaitResult, 1)
	go func() {
		file, err := manager.WaitSegment(context.Background(), requestB.PlaylistPath, -1)
		initResults <- hlsWaitResult{file: file, err: err}
	}()
	assertHLSPending(t, initResults)
	fixture.writeSegment(t, requestB, 33, "B segment 33")
	if err := receiveProgressiveError(t, restarted); err != nil {
		t.Fatalf("RestartHLSAt(33): %v", err)
	}
	initFile := receiveHLSFile(t, initResults)
	defer closeHLSSegment(t, initFile)
	if got := readHLSSegment(t, initFile); got != "B initialization" {
		t.Fatalf("WaitSegment(-1) after seek restart = %q, want B initialization", got)
	}

	// Playback continues: segment 33, then 34, from the replacement job.
	fixture.writeSegment(t, requestB, 34, "B segment 34")
	segment, err := manager.WaitSegment(context.Background(), requestB.PlaylistPath, 33)
	if err != nil {
		t.Fatalf("WaitSegment(33): %v", err)
	}
	defer closeHLSSegment(t, segment)
	if got := readHLSSegment(t, segment); got != "B segment 33" {
		t.Fatalf("WaitSegment(33) = %q, want B segment 33", got)
	}
	nextResults := make(chan hlsWaitResult, 1)
	go func() {
		file, err := manager.WaitSegment(context.Background(), requestB.PlaylistPath, 34)
		nextResults <- hlsWaitResult{file: file, err: err}
	}()
	assertHLSPending(t, nextResults)
	fixture.writeSegment(t, requestB, 35, "B segment 35")
	next := receiveHLSFile(t, nextResults)
	defer closeHLSSegment(t, next)
	if got := readHLSSegment(t, next); got != "B segment 34" {
		t.Fatalf("WaitSegment(34) = %q, want B segment 34", got)
	}
}

// TestRestartHLSAtInitializationJoinsSeekReplacement covers the race in which
// the player's initialization request arrives after a seek restart removed
// the family and before the replacement wrote its initialization segment.
// The request must join the replacement. A restart at segment 0 would stop
// the job that serves the seek target.
func TestRestartHLSAtInitializationJoinsSeekReplacement(t *testing.T) {
	fixture := newHLSFixture(t, "hold")
	manager := fixture.manager(t, clock.NewReal())
	defer closeHLSManager(t, manager)

	request := fixture.requestWithExtension("seek-init-race", 4, ".mp4")
	if err := manager.StartHLS(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	fixture.waitStarted(t, "seek-init-race")
	fixture.writePlaylist(t, request)
	fixture.writeSegment(t, request, -1, "A initialization")
	fixture.writeSegment(t, request, 0, "A segment 0")
	fixture.writeSegment(t, request, 1, "A segment 1")

	if err := manager.RestartHLSAt(context.Background(), request, 33); err != nil {
		t.Fatalf("RestartHLSAt(33): %v", err)
	}
	playlist, err := manager.normalizeOutputPath(request.PlaylistPath)
	if err != nil {
		t.Fatal(err)
	}
	manager.mu.Lock()
	replacement := manager.jobs[playlist]
	manager.mu.Unlock()
	if replacement == nil {
		t.Fatal("RestartHLSAt(33) left no job")
	}
	if _, err := os.Stat(fixture.segmentPath(request, -1)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("initialization segment after restart: %v, want removed with the family", err)
	}

	if err := manager.RestartHLSAt(context.Background(), request, -1); err != nil {
		t.Fatalf("RestartHLSAt(-1): %v", err)
	}
	manager.mu.Lock()
	current := manager.jobs[playlist]
	manager.mu.Unlock()
	if current != replacement {
		t.Fatal("RestartHLSAt(-1) replaced the seek job; want it to join the running job")
	}

	initResults := make(chan hlsWaitResult, 1)
	go func() {
		file, err := manager.WaitSegment(context.Background(), request.PlaylistPath, -1)
		initResults <- hlsWaitResult{file: file, err: err}
	}()
	assertHLSPending(t, initResults)
	fixture.writeSegment(t, request, -1, "B initialization")
	fixture.writeSegment(t, request, 33, "B segment 33")
	initFile := receiveHLSFile(t, initResults)
	defer closeHLSSegment(t, initFile)
	if got := readHLSSegment(t, initFile); got != "B initialization" {
		t.Fatalf("WaitSegment(-1) = %q, want B initialization", got)
	}
}

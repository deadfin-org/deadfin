// SPDX-License-Identifier: GPL-2.0-only

package ffmpeg

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// maxAdoptionWait bounds how long one HLS request waits for the worker of a
// kept family to present the family again (#861). A worker retries its
// connection every 2 s.
const maxAdoptionWait = 10 * time.Second

// KeptFamily names the files of one remote HLS family that the startup wipe
// keeps (#618): the playlist and every file in its directory that starts with
// the playlist stem or the segment prefix.
type KeptFamily struct {
	Playlist       string
	SegmentPattern string
	// WorkerID and LeaseExpires name the worker that keeps the family
	// and the end of its job lease. With a lease, an HLS request for the
	// playlist waits for the adoption before it starts a new family
	// (#861).
	WorkerID     string
	LeaseExpires time.Time
}

// keptAdoption is a kept family that its worker has not presented yet.
// done closes when the family is adopted or the wait ends.
type keptAdoption struct {
	workerID string
	expires  time.Time
	done     chan struct{}
}

// keptAdoptions indexes the kept families that have a lease by their
// canonical playlist path.
func (manager *TranscodeManager) keptAdoptions(keep []KeptFamily) map[string]*keptAdoption {
	adoptions := make(map[string]*keptAdoption)
	for _, kept := range keep {
		if kept.LeaseExpires.IsZero() {
			continue
		}
		playlist, err := manager.normalizeOutputPath(kept.Playlist)
		if err != nil {
			continue
		}
		adoptions[playlist] = &keptAdoption{workerID: kept.WorkerID, expires: kept.LeaseExpires, done: make(chan struct{})}
	}
	return adoptions
}

// awaitAdoption holds an HLS request for a kept family that has no job yet
// (#861). After a server restart a client can ask for the next segment
// before the worker reconnects; a new family then takes the playlist and
// the worker's family is refused and killed. The request waits until the
// family is adopted, the worker reconnects without it, the lease ends, or
// maxAdoptionWait passes. It then continues as before.
func (manager *TranscodeManager) awaitAdoption(ctx context.Context, playlist string) error {
	manager.mu.Lock()
	pending := manager.adoptions[playlist]
	if pending == nil || manager.jobs[playlist] != nil {
		manager.mu.Unlock()
		return nil
	}
	now := manager.options.Clock.Now()
	deadline := now.Add(maxAdoptionWait)
	if pending.expires.Before(deadline) {
		deadline = pending.expires
	}
	if !now.Before(deadline) {
		manager.endAdoptionLocked(playlist)
		manager.mu.Unlock()
		return nil
	}
	timer := manager.options.Clock.NewTimer(deadline.Sub(now))
	manager.mu.Unlock()
	defer timer.Stop()
	if manager.beforeAdoptionWait != nil {
		manager.beforeAdoptionWait()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-pending.done:
		return nil
	case <-timer.C():
	}
	manager.mu.Lock()
	ended := manager.adoptions[playlist] == pending
	if ended {
		manager.endAdoptionLocked(playlist)
	}
	manager.mu.Unlock()
	if logger := manager.options.Logger; ended && logger != nil {
		logger.Info("HLS request stopped waiting for a kept remote family; starting a new one", "output", playlist, "worker", pending.workerID)
	}
	return nil
}

// endAdoptionLocked ends the wait for one kept family.
func (manager *TranscodeManager) endAdoptionLocked(playlist string) {
	if pending := manager.adoptions[playlist]; pending != nil {
		delete(manager.adoptions, playlist)
		close(pending.done)
	}
}

// EndAdoptionWait ends the wait for every kept family of workerID that is
// not adopted (#861). The hub calls it after the worker presented the jobs
// it kept: a family that the worker did not present does not come back.
func (manager *TranscodeManager) EndAdoptionWait(workerID string) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	for playlist, pending := range manager.adoptions {
		if pending.workerID == workerID {
			manager.endAdoptionLocked(playlist)
		}
	}
}

// clearTranscodes empties directory, except the files of keep. Without keep
// it removes the directory, as upstream clears its transcode path on start.
func clearTranscodes(directory string, keep []KeptFamily) error {
	if len(keep) == 0 {
		return os.RemoveAll(directory)
	}
	root, err := filepath.EvalSymlinks(directory)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	type family struct{ stem, prefix string }
	families := make(map[string][]family)
	ancestors := map[string]bool{root: true}
	for _, kept := range keep {
		parent := filepath.Dir(kept.Playlist)
		relative, err := filepath.Rel(root, parent)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.Dir(kept.SegmentPattern) != parent {
			continue
		}
		base := filepath.Base(kept.Playlist)
		prefix := filepath.Base(kept.SegmentPattern)
		if cut := strings.IndexByte(prefix, '%'); cut >= 0 {
			prefix = prefix[:cut]
		}
		stem := strings.TrimSuffix(base, filepath.Ext(base))
		if stem == "" || prefix == "" {
			continue
		}
		families[parent] = append(families[parent], family{stem: stem, prefix: prefix})
		for current := parent; current != root && !ancestors[current]; current = filepath.Dir(current) {
			ancestors[current] = true
		}
	}
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		if entry.IsDir() {
			if ancestors[path] {
				return nil
			}
			if err := os.RemoveAll(path); err != nil {
				return err
			}
			return filepath.SkipDir
		}
		name := entry.Name()
		if !strings.HasPrefix(name, ".") {
			for _, kept := range families[filepath.Dir(path)] {
				if strings.HasPrefix(name, kept.stem) || strings.HasPrefix(name, kept.prefix) {
					return nil
				}
			}
		}
		return os.Remove(path)
	})
}

// AdoptHLSRequest names a remote HLS family that kept running on its host
// across a server restart (#618).
type AdoptHLSRequest struct {
	PlaylistPath         string
	SegmentPattern       string
	PlaySessionID        string
	DeviceID             string
	SegmentLengthSeconds int
	// Remote binds the running remote process. The runner writes its
	// stderr to stderr and closes stderr after the process ends.
	Remote func(stderr io.WriteCloser) RemoteProcess
}

// AdoptHLS registers a running remote family. The family has no rendition
// identity until the first request for its playlist joins it; that request
// supplies the identity, the source authority and the handoff. The idle
// timer, the throttler and the segment cleaner start again here.
func (manager *TranscodeManager) AdoptHLS(ctx context.Context, request AdoptHLSRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if request.Remote == nil {
		return errors.New("adopted HLS family has no remote process")
	}
	if request.SegmentLengthSeconds <= 0 {
		return errors.New("HLS segment length must be positive")
	}
	spec, err := manager.hlsFamily(request.PlaylistPath, request.SegmentPattern)
	if err != nil {
		return err
	}
	spec.playSessionID = request.PlaySessionID
	spec.deviceID = request.DeviceID
	spec.segmentLength = request.SegmentLengthSeconds
	spec.adopted = true
	job := &transcodeJob{
		manager:     manager,
		output:      spec.playlist,
		startDone:   make(chan struct{}),
		waitDone:    make(chan struct{}),
		state:       transcodeStarting,
		idleTimeout: hlsIdleTimeout,
		hls:         spec,
	}
	sidecars, ok := manager.openRemoteSidecars(job)
	if !ok {
		return fmt.Errorf("open the sidecars of adopted HLS family %q", spec.playlist)
	}
	manager.mu.Lock()
	if manager.closed {
		manager.mu.Unlock()
		sidecars.discard()
		return ErrManagerClosed
	}
	if _, exists := manager.jobs[spec.playlist]; exists {
		manager.mu.Unlock()
		sidecars.discard()
		return fmt.Errorf("HLS playlist %q already has a job", spec.playlist)
	}
	remote := request.Remote(sidecars.destination)
	manager.jobs[spec.playlist] = job
	manager.endAdoptionLocked(spec.playlist)
	manager.registerHLSSessionLocked(job)
	manager.attachRemoteLocked(job, remote, sidecars)
	manager.addWorkerLocked()
	close(job.startDone)
	manager.armIdleLocked(job)
	manager.mu.Unlock()
	go manager.wait(job)
	return nil
}

// claimAdoptedLocked gives an adopted family the rendition of the first
// request that joins it.
func claimAdoptedLocked(job *transcodeJob, spec *hlsJob) {
	adopted := job.hls
	if adopted == nil || !adopted.adopted {
		return
	}
	adopted.adopted = false
	adopted.live = spec.live
	adopted.minSegments = spec.minSegments
	adopted.identity = spec.identity
	adopted.facts = spec.facts
	adopted.buildArgs = spec.buildArgs
	adopted.handoff = spec.handoff
	adopted.maxForwardGap = spec.maxForwardGap
	adopted.segmentStarts = spec.segmentStarts
	adopted.runTimeTicks = spec.runTimeTicks
	adopted.startTimeTicks = spec.startTimeTicks
	adopted.mediaSourceID = spec.mediaSourceID
	adopted.sourceRevision = spec.sourceRevision
	adopted.openSource = spec.openSource
	adopted.subtitleSource = spec.subtitleSource
	adopted.externalSubtitleSource = spec.externalSubtitleSource
	adopted.externalSubtitleRevision = spec.externalSubtitleRevision
	adopted.openExternalSubtitle = spec.openExternalSubtitle
	adopted.fontsDir = spec.fontsDir
	adopted.local = spec.local
	adopted.transcodingInfo = spec.transcodingInfo
}

// releaseRemote hands a running remote family to its host at shutdown
// (#618). It reports false when the family is local or the host cannot keep
// it; the caller then stops it.
func (manager *TranscodeManager) releaseRemote(job *transcodeJob) bool {
	manager.mu.Lock()
	remote, isRemote := job.proc.(remoteProcess)
	running := job.state == transcodeRunning && job.hls != nil
	manager.mu.Unlock()
	if !isRemote || !running {
		return false
	}
	releaser, ok := remote.remote.(RemoteReleaser)
	return ok && releaser.Release()
}

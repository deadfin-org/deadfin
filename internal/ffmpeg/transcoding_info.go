// SPDX-License-Identifier: GPL-2.0-only

package ffmpeg

import (
	"strings"

	"github.com/deadfin-org/deadfin/internal/dto"
)

// HasTranscodingJob reports whether a progressive or HLS job for
// playSessionID is starting or running. It is upstream
// TranscodeManager.GetTranscodingJob(playSessionId) is not null: the
// comparison ignores case, and a job leaves the set when it stops.
func (manager *TranscodeManager) HasTranscodingJob(playSessionID string) bool {
	if strings.TrimSpace(playSessionID) == "" {
		return false
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	for _, job := range manager.jobs {
		if !job.active() {
			continue
		}
		id := job.playSessionID
		if job.hls != nil {
			id = job.hls.playSessionID
		}
		if strings.EqualFold(id, playSessionID) {
			return true
		}
	}
	return false
}

// TranscodingInfo returns the session projection of a running HLS job for
// deviceID. Upstream TranscodeManager reports it to the session while the job
// runs and clears it when the job ends (ReportTranscodingProgress,
// OnTranscodeEnd); the session manager matches the device ID ignoring case.
// When several jobs match, the job with the lowest playlist path wins, so the
// answer does not depend on map order.
func (manager *TranscodeManager) TranscodingInfo(deviceID string) (dto.TranscodingInfo, bool) {
	if deviceID == "" {
		return dto.TranscodingInfo{}, false
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	var (
		found    *dto.TranscodingInfo
		playlist string
	)
	for path, job := range manager.jobs {
		if !job.active() || job.hls == nil || job.hls.transcodingInfo == nil || !strings.EqualFold(job.hls.deviceID, deviceID) {
			continue
		}
		if found == nil || path < playlist {
			found, playlist = job.hls.transcodingInfo, path
		}
	}
	if found == nil {
		return dto.TranscodingInfo{}, false
	}
	info := *found
	info.TranscodeReasons = append([]string{}, found.TranscodeReasons...)
	return info, true
}

func (job *transcodeJob) active() bool {
	return (job.state == transcodeStarting || job.state == transcodeRunning) && !job.leaderExited
}

// SPDX-License-Identifier: GPL-2.0-only

package ffmpeg

import (
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
)

// The lookups see a job only while it starts or runs, and match the
// PlaySessionId and device ID ignoring case, as upstream
// TranscodeManager.GetTranscodingJob and SessionManager.ReportTranscodingInfo do.
func TestTranscodeManagerTranscodingJobLookups(t *testing.T) {
	codec := "h264"
	info := &dto.TranscodingInfo{VideoCodec: &codec, IsVideoDirect: true, TranscodeReasons: []string{"ContainerNotSupported"}}
	manager := &TranscodeManager{jobs: map[string]*transcodeJob{
		"/t/hls.m3u8":    {state: transcodeRunning, hls: &hlsJob{playSessionID: "HLS-Session", deviceID: "Device-A", transcodingInfo: info}},
		"/t/progressive": {state: transcodeStarting, playSessionID: "progressive-session"},
		"/t/ended.m3u8":  {state: transcodeExited, hls: &hlsJob{playSessionID: "ended-session", deviceID: "device-b", transcodingInfo: info}},
		"/t/stopping":    {state: transcodeTerminating, playSessionID: "stopping-session"},
	}}

	for id, want := range map[string]bool{
		"hls-session":         true,
		"PROGRESSIVE-SESSION": true,
		"ended-session":       false,
		"stopping-session":    false,
		"":                    false,
		"unknown":             false,
	} {
		if got := manager.HasTranscodingJob(id); got != want {
			t.Errorf("HasTranscodingJob(%q) = %v, want %v", id, got, want)
		}
	}

	got, ok := manager.TranscodingInfo("device-a")
	if !ok || got.VideoCodec == nil || *got.VideoCodec != "h264" || !got.IsVideoDirect {
		t.Fatalf("TranscodingInfo(device-a) = %#v, %v, want the running job's info", got, ok)
	}
	got.TranscodeReasons[0] = "changed"
	if info.TranscodeReasons[0] != "ContainerNotSupported" {
		t.Fatal("TranscodingInfo returned the job's TranscodeReasons slice, want a copy")
	}
	if _, ok := manager.TranscodingInfo("device-b"); ok {
		t.Fatal("TranscodingInfo(device-b) found an exited job")
	}
	if _, ok := manager.TranscodingInfo(""); ok {
		t.Fatal("TranscodingInfo(\"\") found a job")
	}
}

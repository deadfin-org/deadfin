// SPDX-License-Identifier: GPL-2.0-only

package playback_test

import (
	"reflect"
	"testing"
)

// Each HLS job carries the TranscodingInfo upstream
// TranscodeManager.ReportTranscodingProgress puts on the session: the actual
// output codecs (the source codec for a copy), the copy flags, the container,
// and the request's TranscodeReasons.
func TestHLSServiceJobCarriesTranscodingInfo(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		copyVideo                 bool
		wantVideoDirect           bool
		wantVideoCodec, wantAudio string
	}{
		{name: "video copy remux", copyVideo: true, wantVideoDirect: true, wantVideoCodec: "h264", wantAudio: "aac"},
		{name: "video transcode", wantVideoCodec: "h264", wantAudio: "aac"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager := &hlsFakeManager{segment: "segment"}
			service := newHLSService(&progressiveFakeSource{source: progressiveCopySource("/media/goldenrec-input.mp4")}, manager)
			request := hlsTranscodeRequest()
			request.AllowVideoStreamCopy = tc.copyVideo
			request.EnableAutoStreamCopy = tc.copyVideo
			request.VideoBitrate = hlsInt(2_600_000)
			request.TranscodeReasons = "ContainerNotSupported, AudioCodecNotSupported"
			request.SegmentIndex = 0

			if _, err := service.Segment(t.Context(), request); err != nil {
				t.Fatalf("Segment: %v", err)
			}
			info := manager.lastRestart.TranscodingInfo
			if info == nil {
				t.Fatal("HLS job request has no TranscodingInfo")
			}
			if info.IsVideoDirect != tc.wantVideoDirect || info.IsAudioDirect {
				t.Errorf("IsVideoDirect, IsAudioDirect = %v, %v, want %v, false", info.IsVideoDirect, info.IsAudioDirect, tc.wantVideoDirect)
			}
			if got := stringValue(info.VideoCodec); got != tc.wantVideoCodec {
				t.Errorf("VideoCodec = %q, want %q", got, tc.wantVideoCodec)
			}
			if got := stringValue(info.AudioCodec); got != tc.wantAudio {
				t.Errorf("AudioCodec = %q, want %q", got, tc.wantAudio)
			}
			if got := stringValue(info.Container); got != "ts" {
				t.Errorf("Container = %q, want ts", got)
			}
			if got := stringValue(info.HardwareAccelerationType); got != "none" {
				t.Errorf("HardwareAccelerationType = %q, want none", got)
			}
			if want := []string{"ContainerNotSupported", "AudioCodecNotSupported"}; !reflect.DeepEqual(info.TranscodeReasons, want) {
				t.Errorf("TranscodeReasons = %q, want %q", info.TranscodeReasons, want)
			}
			if info.Width == nil || info.Height == nil || info.AudioChannels == nil {
				t.Errorf("Width, Height, AudioChannels = %v, %v, %v, want values", info.Width, info.Height, info.AudioChannels)
			}
		})
	}
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

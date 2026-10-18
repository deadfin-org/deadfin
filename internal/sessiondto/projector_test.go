// SPDX-License-Identifier: GPL-2.0-only

package sessiondto_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/session"
	"github.com/deadfin-org/deadfin/internal/sessiondto"
)

type transcodingFake map[string]dto.TranscodingInfo

func (fake transcodingFake) TranscodingInfo(deviceID string) (dto.TranscodingInfo, bool) {
	info, ok := fake[deviceID]
	return info, ok
}

// A session whose device runs a transcoding job carries TranscodingInfo, as
// upstream SessionInfo.TranscodingInfo does while the job runs. Other
// sessions omit the property.
func TestProjectorFillsTranscodingInfoForTheDevice(t *testing.T) {
	codec := "hevc"
	source := transcodingFake{"tvos-device": {VideoCodec: &codec, IsVideoDirect: true, TranscodeReasons: []string{"ContainerNotSupported"}}}
	projector := sessiondto.New(nil, nil, nil, "server").WithTranscoding(source)

	projected := projector.Project(t.Context(), session.Snapshot{ID: "one", DeviceID: "tvos-device"}, "user")
	if projected.TranscodingInfo == nil {
		t.Fatal("TranscodingInfo is nil for a device with a running job")
	}
	encoded, err := json.Marshal(projected.TranscodingInfo)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"VideoCodec":"hevc"`, `"IsVideoDirect":true`, `"IsAudioDirect":false`, `"TranscodeReasons":["ContainerNotSupported"]`} {
		if !strings.Contains(string(encoded), want) {
			t.Errorf("TranscodingInfo = %s, want %s", encoded, want)
		}
	}

	if other := projector.Project(t.Context(), session.Snapshot{ID: "two", DeviceID: "browser"}, "user"); other.TranscodingInfo != nil {
		t.Errorf("TranscodingInfo = %#v for a device with no job, want nil", other.TranscodingInfo)
	}
	if plain := sessiondto.New(nil, nil, nil, "server").Project(t.Context(), session.Snapshot{ID: "one", DeviceID: "tvos-device"}, "user"); plain.TranscodingInfo != nil {
		t.Errorf("TranscodingInfo = %#v without a source, want nil", plain.TranscodingInfo)
	}
}

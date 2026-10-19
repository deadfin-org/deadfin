// SPDX-License-Identifier: GPL-2.0-only

package playback

import (
	"testing"

	"github.com/deadfin-org/deadfin/internal/encoding"
)

func dynamicHDRTestStream(profile, compatibility int, hdr10Plus bool) *MediaStream {
	transfer := "smpte2084"
	stream := &MediaStream{Type: StreamTypeVideo, Codec: "hevc", ColorTransfer: &transfer, Hdr10PlusPresentFlag: &hdr10Plus}
	if profile != 0 {
		rpu, base := 1, 1
		stream.DvProfile, stream.DvBlSignalCompatibilityID = &profile, &compatibility
		stream.RpuPresentFlag, stream.BlPresentFlag = &rpu, &base
	}
	return stream
}

// TestDynamicHDRRemovalPortsShouldRemoveDynamicHdrMetadata pins the plan
// table of EncodingHelper.ShouldRemoveDynamicHdrMetadata
// (EncodingHelper.cs:1416-1466).
func TestDynamicHDRRemovalPortsShouldRemoveDynamicHdrMetadata(t *testing.T) {
	for _, test := range []struct {
		name      string
		stream    *MediaStream
		requested string
		want      encoding.DynamicHDRRemoval
	}{
		{name: "EL to HDR10 client", stream: dynamicHDRTestStream(7, 6, false), requested: "SDR,HDR10,DOVI", want: encoding.RemoveDolbyVision},
		{name: "EL to EL client", stream: dynamicHDRTestStream(7, 6, false), requested: "HDR10,DOVIWithEL", want: encoding.RemoveNoDynamicHDR},
		{name: "EL to client without HDR10", stream: dynamicHDRTestStream(7, 6, false), requested: "SDR,HLG", want: encoding.RemoveNoDynamicHDR},
		{name: "EL with HDR10+ to EL-only client", stream: dynamicHDRTestStream(7, 6, true), requested: "HDR10,DOVIWithEL", want: encoding.RemoveHDR10Plus},
		{name: "EL with HDR10+ to HDR10 client", stream: dynamicHDRTestStream(7, 6, true), requested: "HDR10,HDR10Plus", want: encoding.RemoveDolbyVision},
		{name: "EL with HDR10+ to full client", stream: dynamicHDRTestStream(7, 6, true), requested: "DOVIWithEL,DOVIWithELHDR10Plus", want: encoding.RemoveDolbyVision},
		{name: "invalid to DOVI client", stream: dynamicHDRTestStream(8, 0, false), requested: "HDR10,DOVI", want: encoding.RemoveDolbyVision},
		{name: "invalid to HDR10 client", stream: dynamicHDRTestStream(8, 0, false), requested: "HDR10", want: encoding.RemoveNoDynamicHDR},
		{name: "8.1 HDR10+ to DOVI client", stream: dynamicHDRTestStream(8, 1, true), requested: "DOVI,DOVIWithHDR10Plus", want: encoding.RemoveHDR10Plus},
		{name: "8.1 HDR10+ to HDR10+ client", stream: dynamicHDRTestStream(8, 1, true), requested: "HDR10Plus,DOVIWithHDR10Plus", want: encoding.RemoveNoDynamicHDR},
		{name: "no request", stream: dynamicHDRTestStream(7, 6, false), want: encoding.RemoveNoDynamicHDR},
		{name: "case-insensitive names", stream: dynamicHDRTestStream(7, 6, false), requested: "hdr10|dovi", want: encoding.RemoveDolbyVision},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := dynamicHDRRemoval(test.stream, test.requested); got != test.want {
				t.Fatalf("dynamicHDRRemoval(%s, %q) = %d, want %d", test.stream.VideoRangeType(), test.requested, got, test.want)
			}
		})
	}
}

// TestProgressiveRangeTypeSupportedPortsCanStreamCopyVideo pins the range
// part of EncodingHelper.CanStreamCopyVideo (EncodingHelper.cs:2470-2524).
func TestProgressiveRangeTypeSupportedPortsCanStreamCopyVideo(t *testing.T) {
	strip := encoding.Capabilities{BSFOptions: []string{"dovi_rpu=strip"}}
	hdr10PlusRemoval := encoding.Capabilities{BSFOptions: []string{"hevc_metadata=remove_hdr10plus"}}
	sdr := &MediaStream{Type: StreamTypeVideo, Codec: "hevc"}
	for _, test := range []struct {
		name      string
		stream    *MediaStream
		requested string
		caps      encoding.Capabilities
		want      bool
	}{
		{name: "no request", stream: dynamicHDRTestStream(7, 6, false), want: true},
		{name: "EL to HDR10 client with strip", stream: dynamicHDRTestStream(7, 6, false), requested: "SDR,HDR10,DOVI", caps: strip, want: true},
		{name: "EL to HDR10 client without strip", stream: dynamicHDRTestStream(7, 6, false), requested: "SDR,HDR10,DOVI", want: false},
		{name: "EL listed", stream: dynamicHDRTestStream(7, 6, false), requested: "DOVIWithEL", want: true},
		{name: "SDR-only list", stream: dynamicHDRTestStream(8, 2, false), requested: "SDR", want: false},
		{name: "SDR source, SDR-only list", stream: sdr, requested: "SDR", want: true},
		{name: "SDR source, HDR10-only list", stream: sdr, requested: "HDR10", want: true},
		{name: "profile 5 without DOVI", stream: dynamicHDRTestStream(5, 0, false), requested: "HDR10,HLG", caps: strip, want: false},
		{name: "8.1 to HDR10 client", stream: dynamicHDRTestStream(8, 1, false), requested: "HDR10", want: true},
		{name: "8.4 to HLG client", stream: dynamicHDRTestStream(8, 4, false), requested: "HLG", want: true},
		{name: "HDR10+ to HDR10 client", stream: dynamicHDRTestStream(0, 0, true), requested: "HDR10", want: true},
		{name: "HDR10 to HLG client", stream: dynamicHDRTestStream(0, 0, false), requested: "SDR,HLG", want: false},
		{name: "8.1 HDR10+ to DOVI client with removal", stream: dynamicHDRTestStream(8, 1, true), requested: "SDR,DOVI", caps: hdr10PlusRemoval, want: true},
		{name: "8.1 HDR10+ to DOVI client without removal", stream: dynamicHDRTestStream(8, 1, true), requested: "SDR,DOVI", want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := progressiveRangeTypeSupported(test.stream, test.requested, test.caps); got != test.want {
				t.Fatalf("progressiveRangeTypeSupported(%s, %q) = %t, want %t", test.stream.VideoRangeType(), test.requested, got, test.want)
			}
		})
	}
}

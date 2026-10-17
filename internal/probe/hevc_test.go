// SPDX-License-Identifier: GPL-2.0-only

package probe

import (
	"fmt"
	"strings"
	"testing"
)

func TestHevcHighTierReadsConfigurationRecordAndSPS(t *testing.T) {
	hvcC := make([]byte, 23)
	hvcC[0] = 1
	hvcC[1] = 0x21
	annexB := []byte{0, 0, 0, 1, 0x42, 0x01, 0x00, 0x20}

	for _, tc := range []struct {
		name      string
		codec     string
		extradata []byte
		want      bool
	}{
		{name: "hvcC high tier", codec: "hevc", extradata: hvcC, want: true},
		{name: "SPS high tier", codec: "h265", extradata: annexB, want: true},
		{name: "SPS main tier", codec: "hevc", extradata: []byte{0, 0, 1, 0x42, 1, 0, 0}, want: false},
		{name: "unknown tier", codec: "hevc", extradata: []byte{1, 0x21}, want: false},
		{name: "other codec", codec: "h264", extradata: hvcC, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := hevcHighTier(tc.codec, testFFprobeHexdump(tc.extradata)); got != tc.want {
				t.Errorf("hevcHighTier() = %t, want %t", got, tc.want)
			}
		})
	}
}

func testFFprobeHexdump(data []byte) string {
	var result strings.Builder
	for offset := 0; offset < len(data); offset += 16 {
		row := data[offset:min(offset+16, len(data))]
		_, _ = fmt.Fprintf(&result, "%08x: ", offset)
		for index := 0; index < 16; index += 2 {
			if index < len(row) {
				_, _ = fmt.Fprintf(&result, "%02x", row[index])
				if index+1 < len(row) {
					_, _ = fmt.Fprintf(&result, "%02x", row[index+1])
				} else {
					result.WriteString("  ")
				}
			} else {
				result.WriteString("    ")
			}
			result.WriteByte(' ')
		}
		result.WriteString(" ")
		for _, value := range row {
			if value >= 0x20 && value <= 0x7e {
				result.WriteByte(value)
			} else {
				result.WriteByte('.')
			}
		}
		result.WriteByte('\n')
	}
	return result.String()
}

// SPDX-License-Identifier: GPL-2.0-only

package probe

import (
	"strconv"
	"strings"
)

// hevcHighTier extracts general_tier_flag from HEVC configuration metadata.
// ffprobe exposes hvcC or SPS extradata as a hexadecimal dump, but does not
// expose the tier as a separate stream field. Unknown or malformed metadata
// safely defaults to the Main tier.
func hevcHighTier(codec, extradata string) bool {
	if !strings.EqualFold(codec, "hevc") && !strings.EqualFold(codec, "h265") {
		return false
	}
	data := ffprobeHexdumpBytes(extradata)
	if len(data) >= 23 && data[0] == 1 {
		// HEVCDecoderConfigurationRecord.general_profile_tier_level starts
		// with general_profile_space (2 bits), general_tier_flag (1 bit), and
		// general_profile_idc (5 bits).
		return data[1]&0x20 != 0
	}
	if high, ok := annexBSPSHighTier(data); ok {
		return high
	}
	if high, ok := hevcSPSHighTier(data); ok {
		return high
	}
	return false
}

// ffprobeHexdumpBytes decodes the offset-prefixed rows produced by
// ffprobe's -show_data option. The optional ASCII column is separated from
// the hex bytes by a double space.
func ffprobeHexdumpBytes(dump string) []byte {
	var result []byte
	for _, line := range strings.Split(dump, "\n") {
		line = strings.TrimSpace(line)
		colon := strings.IndexByte(line, ':')
		if colon < 0 {
			continue
		}
		if _, err := strconv.ParseUint(strings.TrimSpace(line[:colon]), 16, 32); err != nil {
			continue
		}
		hexPart := line[colon+1:]
		if ascii := strings.Index(hexPart, "  "); ascii >= 0 {
			hexPart = hexPart[:ascii]
		}
		for _, group := range strings.Fields(hexPart) {
			if len(group)%2 != 0 {
				break
			}
			for offset := 0; offset < len(group); offset += 2 {
				value, err := strconv.ParseUint(group[offset:offset+2], 16, 8)
				if err != nil {
					return result
				}
				result = append(result, byte(value))
			}
		}
	}
	return result
}

func annexBSPSHighTier(data []byte) (bool, bool) {
	for offset := 0; offset < len(data); {
		start, prefix := nextHEVCStartCode(data, offset)
		if start < 0 {
			return false, false
		}
		nalStart := start + prefix
		nextStart, _ := nextHEVCStartCode(data, nalStart)
		nalEnd := len(data)
		if nextStart >= 0 {
			nalEnd = nextStart
		}
		if high, ok := hevcSPSHighTier(data[nalStart:nalEnd]); ok {
			return high, true
		}
		if nextStart < 0 {
			return false, false
		}
		offset = nextStart
	}
	return false, false
}

func nextHEVCStartCode(data []byte, offset int) (int, int) {
	for index := offset; index+3 <= len(data); index++ {
		if data[index] != 0 || data[index+1] != 0 {
			continue
		}
		if data[index+2] == 1 {
			return index, 3
		}
		if index+4 <= len(data) && data[index+2] == 0 && data[index+3] == 1 {
			return index, 4
		}
	}
	return -1, 0
}

func hevcSPSHighTier(nal []byte) (bool, bool) {
	if len(nal) < 4 || (nal[0]>>1)&0x3f != 33 {
		return false, false
	}

	// SPS profile_tier_level follows the one-byte SPS header fields. Remove
	// emulation-prevention bytes before reading its second RBSP byte.
	rbsp := make([]byte, 0, len(nal)-2)
	zeroCount := 0
	for _, value := range nal[2:] {
		if zeroCount >= 2 && value == 3 {
			zeroCount = 0
			continue
		}
		rbsp = append(rbsp, value)
		if value == 0 {
			zeroCount++
		} else {
			zeroCount = 0
		}
	}
	if len(rbsp) < 2 {
		return false, false
	}
	return rbsp[1]&0x20 != 0, true
}

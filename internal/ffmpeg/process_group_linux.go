// SPDX-License-Identifier: GPL-2.0-only

package ffmpeg

import (
	"bytes"
	"os"
	"strconv"
)

// processGroupHasLiveMember reports whether group pgid has a member that is
// not a zombie. If /proc cannot be read, it reports true.
func processGroupHasLiveMember(pgid int) bool {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return true
	}
	for _, entry := range entries {
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		stat, err := os.ReadFile("/proc/" + entry.Name() + "/stat")
		if err != nil {
			continue
		}
		// The comm field can hold spaces and parentheses, so the fields
		// start after its last closing parenthesis: state, ppid, pgrp.
		end := bytes.LastIndexByte(stat, ')')
		if end < 0 {
			continue
		}
		fields := bytes.Fields(stat[end+1:])
		if len(fields) < 3 {
			continue
		}
		group, err := strconv.Atoi(string(fields[2]))
		if err != nil || group != pgid {
			continue
		}
		if state := string(fields[0]); state != "Z" && state != "X" {
			return true
		}
	}
	return false
}

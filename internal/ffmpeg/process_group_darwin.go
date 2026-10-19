// SPDX-License-Identifier: GPL-2.0-only

package ffmpeg

import "golang.org/x/sys/unix"

// darwinZombieState is SZOMB from <sys/proc.h>.
const darwinZombieState = 5

// processGroupHasLiveMember reports whether group pgid has a member that is
// not a zombie. If the process table cannot be read, it reports true.
func processGroupHasLiveMember(pgid int) bool {
	procs, err := unix.SysctlKinfoProcSlice("kern.proc.pgrp", pgid)
	if err != nil {
		return true
	}
	for _, proc := range procs {
		if proc.Proc.P_stat != darwinZombieState {
			return true
		}
	}
	return false
}

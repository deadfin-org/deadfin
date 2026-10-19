// SPDX-License-Identifier: GPL-2.0-only

//go:build !linux && !darwin

package ffmpeg

// processGroupHasLiveMember reports true: this platform has no process-state
// lookup here, so a signalable group counts as live.
func processGroupHasLiveMember(int) bool {
	return true
}

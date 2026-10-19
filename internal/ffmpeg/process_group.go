// SPDX-License-Identifier: GPL-2.0-only

package ffmpeg

import (
	"errors"
	"fmt"
	"syscall"
	"time"

	"github.com/deadfin-org/deadfin/internal/clock"
)

const (
	// processGroupReapTimeout bounds the wait for SIGKILLed group members to
	// exit. A killed process exits when the kernel next schedules it, so a
	// loaded host can need more than a few milliseconds.
	processGroupReapTimeout = 2 * time.Second
	// processGroupReapMaxPoll caps the poll interval of the reap wait.
	processGroupReapMaxPoll = 20 * time.Millisecond
)

// reapProcessGroup kills the process group of an exited ffmpeg leader and
// waits until no live member remains. It measures the wait on real time: it
// waits for the kernel, not for manager timers, so the manager's injected
// clock does not apply.
func reapProcessGroup(pid int) error {
	return awaitProcessGroupGone(clock.NewReal(), pid, processGroupReapTimeout, killProcessGroup)
}

// awaitProcessGroupGone calls probe until it reports group pid gone, with a
// poll interval that doubles up to processGroupReapMaxPoll. It gives up when
// timeout has passed on clk.
func awaitProcessGroupGone(clk clock.Clock, pid int, timeout time.Duration, probe func(int) (bool, error)) error {
	deadline := clk.Now().Add(timeout)
	poll := time.Millisecond
	for {
		gone, err := probe(pid)
		if err != nil {
			return err
		}
		if gone {
			return nil
		}
		if !clk.Now().Before(deadline) {
			return fmt.Errorf("ffmpeg process group %d survived SIGKILL", pid)
		}
		timer := clk.NewTimer(poll)
		<-timer.C()
		poll = min(2*poll, processGroupReapMaxPoll)
	}
}

// killProcessGroup sends SIGKILL to group pid and reports whether the group
// has no live member. A zombie member counts as gone: it runs no code and
// holds no files, and only its parent can collect it. The leader's parent is
// the manager, but other members reparent to PID 1 or to a subreaper, which
// can collect them late on a loaded host or never in a container whose PID 1
// does not call wait.
func killProcessGroup(pid int) (bool, error) {
	err := syscall.Kill(-pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return true, nil
	}
	if err != nil && !errors.Is(err, syscall.EPERM) {
		return false, err
	}
	return !processGroupHasLiveMember(pid), nil
}

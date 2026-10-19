// SPDX-License-Identifier: GPL-2.0-only

package ffmpeg

import (
	"errors"
	"os/exec"
	"runtime"
	"syscall"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/clock"
)

// TestReapProcessGroupTreatsZombieMembersAsGone keeps an exited group member
// uncollected, as a busy or absent orphan reaper does. The kernel still
// counts the zombie as a group member, but the group has no live member, so
// the reap must succeed at once.
func TestReapProcessGroupTreatsZombieMembersAsGone(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("zombie detection is implemented for Linux and Darwin")
	}
	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	defer func() { _ = cmd.Wait() }()

	// Do not collect the child, so that it stays a zombie.
	waitForProcessGroupZombie(t, pid)
	if err := syscall.Kill(-pid, 0); errors.Is(err, syscall.ESRCH) {
		t.Fatalf("kill(-%d, 0) = ESRCH before the zombie was collected", pid)
	}
	fake := clock.NewFake(time.Date(2026, time.October, 18, 12, 0, 0, 0, time.UTC))
	if err := awaitProcessGroupGone(fake, pid, 0, killProcessGroup); err != nil {
		t.Fatalf("reap zombie-only group: %v", err)
	}
}

// TestAwaitProcessGroupGoneWaitsOnInjectedClock verifies that the reap wait
// polls on the injected clock with a growing interval and gives up only when
// the timeout has passed on that clock.
func TestAwaitProcessGroupGoneWaitsOnInjectedClock(t *testing.T) {
	base := clock.NewFake(time.Date(2026, time.October, 18, 12, 0, 0, 0, time.UTC))
	observed := newProgressiveObservedClock(base)
	probes := 0
	alive := func(int) (bool, error) {
		probes++
		return false, nil
	}
	result := make(chan error, 1)
	go func() { result <- awaitProcessGroupGone(observed, 42, 10*time.Millisecond, alive) }()

	// Polls of 1, 2, 4 and 8 ms pass the 10 ms deadline.
	for _, poll := range []time.Duration{time.Millisecond, 2 * time.Millisecond, 4 * time.Millisecond, 8 * time.Millisecond} {
		observed.waitForTimer(t, poll)
		select {
		case err := <-result:
			t.Fatalf("reap ended before %s poll: %v", poll, err)
		default:
		}
		base.Advance(poll)
	}
	deadline := time.NewTimer(fixtureTimeout)
	defer deadline.Stop()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("reap of a live group succeeded")
		}
	case <-deadline.C:
		t.Fatal("reap did not give up after the timeout")
	}
	if probes != 5 {
		t.Fatalf("probes = %d, want 5", probes)
	}
}

func waitForProcessGroupZombie(t *testing.T, pgid int) {
	t.Helper()
	deadline := time.NewTimer(fixtureTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for processGroupHasLiveMember(pgid) {
		select {
		case <-deadline.C:
			t.Fatalf("process group %d still has a live member", pgid)
		case <-ticker.C:
		}
	}
}

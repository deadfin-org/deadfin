// SPDX-License-Identifier: GPL-2.0-only

// Command subreaper runs a command with orphan reaping, for CI runners whose
// PID 1 is not an init.
//
// Why this exists. The jailed Actions runner starts its job container with
// `/bin/sleep 10800` as PID 1. A sleep never calls wait(), so any process
// orphaned during a job reparents to it and stays a zombie for the life of the
// job — measured on a self-hosted runner, where `git` and `go` from earlier
// steps were already zombies before any test ran.
//
// That breaks the internal/ffmpeg reaping tests, which decide a process is
// gone with syscall.Kill(pid, 0) == ESRCH (see waitForProcessExit in
// internal/ffmpeg/prober_test.go). kill(pid, 0) SUCCEEDS against a zombie: the
// PID stays in the table until someone collects its exit status. The tests
// were therefore reporting "process N survived cleanup" about a corpse.
// Deadfin's own cleanup is correct — the group leader is reaped and every
// survivor is in state Z — so the fix belongs in the job, not in
// internal/ffmpeg, and emphatically not in relaxing the tests to tolerate
// zombies.
//
// What it does: sets PR_SET_CHILD_SUBREAPER so orphaned descendants reparent
// to this process rather than to PID 1, then reaps them. Both halves are
// required. Setting the flag alone only changes *where* the corpses pile up —
// without the wait() loop below they are still zombies and the tests still
// fail, just parented somewhere else.
//
// A CI runner whose nspawn guest runs systemd as PID 1 reaps orphans itself;
// the subreaper is harmless there.
//
// Test cleanup. The command runs with a private TMPDIR that is removed when
// the command exits, pass or fail. After the command exits, every process
// still parented to the subreaper is killed and reaped before the TMPDIR goes.
// A test helper that a dead test binary leaves behind therefore cannot keep
// writing files after the run.
//go:build linux

package main

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

// PR_SET_CHILD_SUBREAPER. Not exposed by the syscall package.
const prSetChildSubreaper = 36

// leftoverDeadline bounds the kill-and-reap pass after the command exits.
const leftoverDeadline = 10 * time.Second

func main() {
	os.Exit(run(os.Args[1:]))
}

// run runs the command in args and returns the exit code for the wrapper.
func run(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: subreaper <command> [args...]")
		return 2
	}

	// Register for SIGCHLD before starting the child, so an exit that happens
	// immediately cannot be missed.
	sigchld := make(chan os.Signal, 128)
	signal.Notify(sigchld, syscall.SIGCHLD)
	stop := make(chan os.Signal, 2)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigchld)
	defer signal.Stop(stop)

	if _, _, errno := syscall.Syscall(syscall.SYS_PRCTL, prSetChildSubreaper, 1, 0); errno != 0 {
		// Not fatal. On a runner that already has a real init this changes
		// nothing, and failing the job here would hide the actual test result.
		fmt.Fprintf(os.Stderr, "subreaper: PR_SET_CHILD_SUBREAPER failed: %v (continuing)\n", errno)
	}

	tempDir, err := os.MkdirTemp("", "deadfin-test-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "subreaper: create TMPDIR: %v\n", err)
		return 1
	}

	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = append(os.Environ(), "TMPDIR="+tempDir)
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "subreaper: %v\n", err)
		_ = os.RemoveAll(tempDir)
		return 127
	}
	target := cmd.Process.Pid

	code, orphans := waitTarget(target, sigchld, stop)
	killed, reaped := killLeftovers()
	orphans += reaped
	if orphans > 0 {
		fmt.Fprintf(os.Stderr, "subreaper: reaped %d orphan(s)\n", orphans)
	}
	if killed > 0 {
		fmt.Fprintf(os.Stderr, "subreaper: killed %d leftover process(es)\n", killed)
	}
	if err := os.RemoveAll(tempDir); err != nil {
		fmt.Fprintf(os.Stderr, "subreaper: remove TMPDIR %s: %v\n", tempDir, err)
	}
	return code
}

// waitTarget reaps children until the target exits and returns its exit code
// and the number of other children reaped. SIGINT and SIGTERM go to the target.
//
// Reap everything, not just the target: orphans are the entire point. Note
// cmd.Wait() is deliberately never called — it would race this loop for the
// target's exit status, and whichever lost would report an error.
func waitTarget(target int, sigchld, stop <-chan os.Signal) (code, orphans int) {
	for {
		select {
		case sig := <-stop:
			_ = syscall.Kill(target, sig.(syscall.Signal))
			continue
		case <-sigchld:
		}
		for {
			var status syscall.WaitStatus
			pid, err := syscall.Wait4(-1, &status, syscall.WNOHANG, nil)
			if err == syscall.EINTR {
				continue
			}
			// pid 0 = children exist but none have exited yet; -1 with ECHILD =
			// none left. Either way, wait for the next SIGCHLD.
			if pid <= 0 || err != nil {
				break
			}
			if pid != target {
				orphans++
				continue
			}
			if status.Signaled() {
				return 128 + int(status.Signal()), orphans
			}
			return status.ExitStatus(), orphans
		}
	}
}

// killLeftovers kills every remaining child until none is left. A killed
// child's own children reparent to the subreaper, so each pass looks again.
func killLeftovers() (killed, reaped int) {
	self := os.Getpid()
	deadline := time.Now().Add(leftoverDeadline)
	seen := map[int]bool{}
	for {
		children := childrenOf(self)
		for _, pid := range children {
			if !seen[pid] {
				seen[pid] = true
				killed++
			}
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
		for {
			pid, err := syscall.Wait4(-1, nil, syscall.WNOHANG, nil)
			if err == syscall.EINTR {
				continue
			}
			if pid <= 0 || err != nil {
				break
			}
			reaped++
		}
		if len(children) == 0 {
			return killed, reaped
		}
		if time.Now().After(deadline) {
			fmt.Fprintf(os.Stderr, "subreaper: %d process(es) still alive after %s\n", len(children), leftoverDeadline)
			return killed, reaped
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// childrenOf lists the live (not zombie) processes whose parent is pid, from
// /proc. Zombies are collected by the Wait4 loop.
func childrenOf(pid int) []int {
	stats, _ := filepath.Glob("/proc/[0-9]*/stat")
	var children []int
	for _, path := range stats {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		// The command name is in parentheses and can hold spaces; the fields
		// after the last ')' are state, then the parent PID.
		end := bytes.LastIndexByte(data, ')')
		if end < 0 {
			continue
		}
		fields := bufio.NewScanner(bytes.NewReader(data[end+1:]))
		fields.Split(bufio.ScanWords)
		if !fields.Scan() || fields.Text() == "Z" || !fields.Scan() {
			continue
		}
		parent, err := strconv.Atoi(fields.Text())
		if err != nil || parent != pid {
			continue
		}
		child, err := strconv.Atoi(filepath.Base(filepath.Dir(path)))
		if err == nil {
			children = append(children, child)
		}
	}
	return children
}

// SPDX-License-Identifier: GPL-2.0-only

//go:build unix && !linux

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
)

// On Unix systems other than Linux there is no child subreaper. The wrapper
// still runs the command with a private TMPDIR, kills the command's process
// group when it exits, and removes the TMPDIR. A helper in its own process
// group can outlive the run, but the directory it writes into is gone.
func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: subreaper <command> [args...]")
		os.Exit(2)
	}
	stop := make(chan os.Signal, 2)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	tempDir, err := os.MkdirTemp("", "deadfin-test-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "subreaper: create TMPDIR: %v\n", err)
		os.Exit(1)
	}

	cmd := exec.Command(os.Args[1], os.Args[2:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = append(os.Environ(), "TMPDIR="+tempDir)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "subreaper: %v\n", err)
		_ = os.RemoveAll(tempDir)
		os.Exit(127)
	}
	go func() {
		for sig := range stop {
			_ = cmd.Process.Signal(sig)
		}
	}()

	code := 0
	if err := cmd.Wait(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			fmt.Fprintf(os.Stderr, "subreaper: %v\n", err)
			code = 1
		} else if status, ok := exit.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			code = 128 + int(status.Signal())
		} else {
			code = exit.ExitCode()
		}
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if err := os.RemoveAll(tempDir); err != nil {
		fmt.Fprintf(os.Stderr, "subreaper: remove TMPDIR %s: %v\n", tempDir, err)
	}
	os.Exit(code)
}

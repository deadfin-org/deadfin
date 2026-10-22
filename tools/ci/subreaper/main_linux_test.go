// SPDX-License-Identifier: GPL-2.0-only

//go:build linux

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const helperEnv = "SUBREAPER_TEST_HELPER"

// TestMain lets the test binary act as the wrapped command ("parent") and as
// a runaway file writer in its own process group ("writer").
func TestMain(m *testing.M) {
	switch os.Getenv(helperEnv) {
	case "parent":
		os.Exit(runParentHelper())
	case "writer":
		runWriterHelper()
	}
	os.Exit(m.Run())
}

func runParentHelper() int {
	out := os.Getenv("SUBREAPER_TEST_OUT")
	writer := exec.Command(os.Args[0])
	writer.Env = append(os.Environ(), helperEnv+"=writer")
	writer.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := writer.Start(); err != nil {
		return 100
	}
	record := fmt.Sprintf("%d\n%s\n", writer.Process.Pid, os.Getenv("TMPDIR"))
	if err := os.WriteFile(filepath.Join(out, "record"), []byte(record), 0o600); err != nil {
		return 101
	}
	time.Sleep(200 * time.Millisecond)
	return 3
}

func runWriterHelper() {
	directory := os.Getenv("TMPDIR")
	for index := 0; ; index++ {
		_ = os.WriteFile(filepath.Join(directory, "segment"+strconv.Itoa(index)), []byte("x"), 0o600)
		time.Sleep(5 * time.Millisecond)
	}
}

// A command that exits and leaves a writer in its own process group: the
// wrapper returns the command's exit code, kills the writer, and removes the
// private TMPDIR.
func TestRunKillsLeftoversAndRemovesTempDir(t *testing.T) {
	out := t.TempDir()
	t.Setenv(helperEnv, "parent")
	t.Setenv("SUBREAPER_TEST_OUT", out)

	if code := run([]string{os.Args[0]}); code != 3 {
		t.Fatalf("run exit code = %d, want 3", code)
	}

	data, err := os.ReadFile(filepath.Join(out, "record"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("helper record = %q", data)
	}
	pid, err := strconv.Atoi(lines[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		t.Fatalf("writer %d survived the wrapper: kill(pid, 0) = %v", pid, err)
	}
	tempDir := lines[1]
	if !strings.HasPrefix(filepath.Base(tempDir), "deadfin-test-") {
		t.Fatalf("command TMPDIR = %q, want the wrapper's private directory", tempDir)
	}
	if _, err := os.Stat(tempDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("private TMPDIR %s remains: stat error = %v", tempDir, err)
	}
}

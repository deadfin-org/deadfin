// SPDX-License-Identifier: GPL-2.0-only

// Command buildinfo-gen stages a replacement internal/buildinfo/buildinfo.go
// outside the module tree and writes a go build -overlay file that maps the
// in-tree path to it. The tree is never modified.
//
// Usage: buildinfo-gen -o <staging-dir>
//
// Inputs come from the environment only.
// Explicit mode is selected when any of REVISION, VERSION, CHANNEL, DIRTY is
// present; discover mode reads Git state of the working directory.
package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/deadfin-org/deadfin/internal/buildinfo"
)

const (
	modulePath       = "github.com/deadfin-org/deadfin"
	inTreeFile       = "internal/buildinfo/buildinfo.go"
	maxEpoch         = 253402300799 // 9999-12-31T23:59:59Z
	usageText        = "usage: buildinfo-gen -o <staging-dir>\n"
	stagedFileName   = "buildinfo.go"
	overlayFileName  = "overlay.json"
	explicitRevision = "REVISION"
	explicitVersion  = "VERSION"
	explicitChannel  = "CHANNEL"
	explicitDirty    = "DIRTY"
)

// isRCNumber mirrors the rc suffix of tools/image-publication: [1-9][0-9]*.
func isRCNumber(s string) bool {
	if s == "" || s[0] == '0' {
		return false
	}
	return strings.Trim(s, "0123456789") == ""
}

// isSemver mirrors tools/image-publication:6:
// ^(v)?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$
func isSemver(s string) bool {
	parts := strings.Split(strings.TrimPrefix(s, "v"), ".")
	if len(parts) != 3 {
		return false
	}
	for _, p := range parts {
		if p == "" || (p != "0" && p[0] == '0') {
			return false
		}
		for i := 0; i < len(p); i++ {
			if p[i] < '0' || p[i] > '9' {
				return false
			}
		}
	}
	return true
}

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}

// run is the testable entry: it returns the process exit code.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	staging, ok := parseArgs(args)
	if !ok {
		_, _ = io.WriteString(stderr, usageText)
		return 2
	}
	if err := generate(ctx, staging, stdout); err != nil {
		fmt.Fprintf(stderr, "buildinfo-gen: %v\n", err)
		return 1
	}
	return 0
}

// parseArgs accepts exactly "-o <dir>" (or "-o=<dir>") and nothing else.
func parseArgs(args []string) (string, bool) {
	switch len(args) {
	case 1:
		if strings.HasPrefix(args[0], "-o=") && len(args[0]) > 3 {
			return args[0][3:], true
		}
	case 2:
		if args[0] == "-o" && args[1] != "" && !strings.HasPrefix(args[1], "-") {
			return args[1], true
		}
	}
	return "", false
}

func generate(ctx context.Context, staging string, stdout io.Writer) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	root, err := filepath.Abs(cwd)
	if err != nil {
		return err
	}
	if err := checkModuleRoot(root); err != nil {
		return err
	}
	stagingAbs, err := filepath.Abs(staging)
	if err != nil {
		return err
	}
	info, err := os.Stat(stagingAbs)
	if err != nil {
		return fmt.Errorf("staging directory: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("staging path %s is not a directory", stagingAbs)
	}
	if stagingAbs == root || strings.HasPrefix(stagingAbs, root+string(filepath.Separator)) {
		return fmt.Errorf("staging directory %s is inside the module root %s; the tree must stay unmodified", stagingAbs, root)
	}

	// Mode is chosen once, before any value is read.
	var id buildinfo.Identity
	if explicitMode() {
		id, err = readExplicit()
	} else {
		id, err = discover(ctx, root)
	}
	if err != nil {
		return err
	}
	id.Component = buildinfo.Component
	id.BuildTime, err = buildTime()
	if err != nil {
		return err
	}
	if err := buildinfo.Validate(id); err != nil {
		return err
	}

	stagedPath := filepath.Join(stagingAbs, stagedFileName)
	overlayPath := filepath.Join(stagingAbs, overlayFileName)
	inTree := filepath.Join(root, filepath.FromSlash(inTreeFile))
	overlay := fmt.Sprintf("{\"Replace\":{%s:%s}}\n", strconv.Quote(inTree), strconv.Quote(stagedPath))
	if err := writeAtomic(stagedPath, renderSource(id)); err != nil {
		return err
	}
	if err := writeAtomic(overlayPath, overlay); err != nil {
		_ = os.Remove(stagedPath)
		return err
	}
	fmt.Fprintf(stdout, "component=%s\nversion=%s\nchannel=%s\nrevision=%s\ndirty=%t\nbuild_time=%s\n", id.Component, id.Version, id.Channel, id.Revision, id.Dirty, id.BuildTime)
	return nil
}

func checkModuleRoot(root string) error {
	file, err := os.Open(filepath.Join(root, "go.mod"))
	if err != nil {
		return fmt.Errorf("run buildinfo-gen from the module root: %w", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "module ") {
			if strings.TrimSpace(strings.TrimPrefix(line, "module ")) == modulePath {
				return nil
			}
			return fmt.Errorf("go.mod in %s is not module %s", root, modulePath)
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return fmt.Errorf("go.mod in %s has no module line", root)
}

func explicitMode() bool {
	for _, name := range []string{explicitRevision, explicitVersion, explicitChannel, explicitDirty} {
		if _, ok := os.LookupEnv(name); ok {
			return true
		}
	}
	return false
}

// readExplicit implements the explicit mode of docs/09 §4.1. Presence counts
// even for the empty string (DIRTY= is a legal false).
func readExplicit() (buildinfo.Identity, error) {
	var missing []string
	values := map[string]string{}
	for _, name := range []string{explicitRevision, explicitVersion, explicitChannel, explicitDirty} {
		value, ok := os.LookupEnv(name)
		if !ok {
			missing = append(missing, name)
			continue
		}
		values[name] = value
	}
	if len(missing) > 0 {
		return buildinfo.Identity{}, fmt.Errorf("rule 9: explicit mode requires every variable; missing %s", strings.Join(missing, ", "))
	}
	dirty, err := parseDirty(values[explicitDirty])
	if err != nil {
		return buildinfo.Identity{}, err
	}
	revision := values[explicitRevision]
	if revision != buildinfo.UnknownRevision && !buildinfo.IsRevision(revision) {
		return buildinfo.Identity{}, fmt.Errorf("rule 2: REVISION %q is neither unknown nor 40 lowercase hex characters", revision)
	}
	return buildinfo.Identity{
		Version:  values[explicitVersion],
		Channel:  values[explicitChannel],
		Revision: revision,
		Dirty:    dirty,
	}, nil
}

// parseDirty accepts exactly the DIRTY table of docs/09 §4.1.
func parseDirty(raw string) (bool, error) {
	switch strings.TrimSpace(raw) {
	case "false", "0", "":
		return false, nil
	case "true", "1", "-dirty":
		return true, nil
	}
	return false, fmt.Errorf("rule 10: DIRTY %q is not one of false, 0, empty, true, 1, -dirty", raw)
}

// buildTime reads SOURCE_DATE_EPOCH in both modes (rule 11).
func buildTime() (string, error) {
	raw, ok := os.LookupEnv("SOURCE_DATE_EPOCH")
	if !ok {
		return "", nil
	}
	if raw == "" || strings.TrimLeft(raw, "0123456789") != "" {
		return "", fmt.Errorf("rule 11: SOURCE_DATE_EPOCH %q is not a non-negative decimal integer", raw)
	}
	epoch, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || epoch > maxEpoch {
		return "", fmt.Errorf("rule 11: SOURCE_DATE_EPOCH %q is outside 0..%d", raw, maxEpoch)
	}
	return time.Unix(epoch, 0).UTC().Format(time.RFC3339), nil
}

// git runs one Git command in root with parent-repository leakage removed.
func git(ctx context.Context, root string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = root
	env := os.Environ()
	filtered := env[:0]
	for _, entry := range env {
		if strings.HasPrefix(entry, "GIT_DIR=") || strings.HasPrefix(entry, "GIT_WORK_TREE=") {
			continue
		}
		filtered = append(filtered, entry)
	}
	cmd.Env = filtered
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return stdout.String(), nil
}

// discover implements the discover mode of docs/09 §4.1 in the fixed order:
// dirty first, then revision, then ref.
func discover(ctx context.Context, root string) (buildinfo.Identity, error) {
	id := buildinfo.Identity{Channel: buildinfo.ChannelLocal, Version: buildinfo.LocalVersion, Revision: buildinfo.UnknownRevision, Dirty: true}

	status, err := git(ctx, root, "status", "--porcelain", "--untracked-files=all")
	id.Dirty = err != nil || strings.TrimSpace(status) != ""

	head, err := git(ctx, root, "rev-parse", "HEAD")
	head = strings.TrimSpace(head)
	if err != nil || !buildinfo.IsRevision(head) {
		id.Dirty = true
		return id, nil
	}
	id.Revision = head

	ref := ""
	if branch, err := git(ctx, root, "symbolic-ref", "--short", "HEAD"); err == nil {
		ref = strings.TrimSpace(branch)
	} else if tag, err := git(ctx, root, "describe", "--tags", "--exact-match", "HEAD"); err == nil {
		ref = "refs/tags/" + strings.TrimSpace(tag)
	}
	var committed time.Time
	if ct, err := git(ctx, root, "show", "-s", "--format=%ct", "HEAD"); err == nil {
		if sec, err := strconv.ParseInt(strings.TrimSpace(ct), 10, 64); err == nil {
			committed = time.Unix(sec, 0)
		}
	}
	id.Channel, id.Version = classifyRef(ref, head, committed)
	return id, nil
}

// classifyRef is the ref-to-channel table of docs/09 §4.1, identical to the
// case statement in tools/image-publication. A release-line ref needs the
// commit time; without it the build is local.
func classifyRef(ref, sha string, committed time.Time) (channel, version string) {
	sha12 := sha[:12]
	switch {
	case ref == "master" || ref == "refs/heads/master":
		return buildinfo.ChannelMaster, "dev-" + sha12
	case strings.HasPrefix(ref, "train/") || strings.HasPrefix(ref, "refs/heads/train/"):
		train := strings.TrimPrefix(strings.TrimPrefix(ref, "refs/heads/"), "train/")
		if isSemver(train) {
			return buildinfo.ChannelTrain, train + "-dev-" + sha12
		}
	case strings.HasPrefix(ref, "v") || strings.HasPrefix(ref, "refs/heads/v"):
		line := strings.TrimPrefix(strings.TrimPrefix(ref, "refs/heads/"), "v")
		if isSemver(line+".0") && !strings.HasPrefix(line, "v") && !committed.IsZero() {
			return buildinfo.ChannelDev, line + ".0-dev." + committed.UTC().Format("20060102150405") + "-" + sha12
		}
	case strings.HasPrefix(ref, "refs/tags/"):
		tag := strings.TrimPrefix(ref, "refs/tags/")
		if isSemver(tag) {
			return buildinfo.ChannelRelease, tag
		}
		if core, n, ok := strings.Cut(tag, "-rc."); ok && isSemver(core) && isRCNumber(n) {
			return buildinfo.ChannelRC, strings.TrimPrefix(tag, "v")
		}
	}
	return buildinfo.ChannelLocal, buildinfo.LocalVersion
}

// renderSource emits the same shape as the checked-in file.
func renderSource(id buildinfo.Identity) string {
	var b strings.Builder
	b.WriteString("// Package buildinfo carries the immutable server build identity.\n")
	b.WriteString("// The checked-in file is the unknown/local default. tools/buildinfo-gen\n")
	b.WriteString("// stages a replacement out of tree and go build consumes it via -overlay.\n")
	b.WriteString("package buildinfo\n\nconst (\n")
	fmt.Fprintf(&b, "\tComponent           = %s\n", strconv.Quote(id.Component))
	fmt.Fprintf(&b, "\tVersion             = %s\n", strconv.Quote(id.Version))
	fmt.Fprintf(&b, "\tChannel             = %s\n", strconv.Quote(id.Channel))
	fmt.Fprintf(&b, "\tRevision            = %s\n", strconv.Quote(id.Revision))
	fmt.Fprintf(&b, "\tDirty               = %t\n", id.Dirty)
	fmt.Fprintf(&b, "\tBuildTime           = %s\n", strconv.Quote(id.BuildTime))
	fmt.Fprintf(&b, "\tCompatibilityTarget = %s\n", strconv.Quote(buildinfo.CompatibilityTarget))
	fmt.Fprintf(&b, "\tAPIVersion          = %d\n", buildinfo.APIVersion)
	b.WriteString(")\n\n// Capabilities returns a fresh slice on every call.\nfunc Capabilities() []string { return []string{")
	for i, capability := range buildinfo.Capabilities() {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(strconv.Quote(capability))
	}
	b.WriteString("} }\n")
	return b.String()
}

// writeAtomic writes to a temporary name in the same directory, then renames.
func writeAtomic(path, content string) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	_, werr := io.WriteString(tmp, content)
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return err
	}
	return nil
}

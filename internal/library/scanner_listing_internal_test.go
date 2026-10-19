// SPDX-License-Identifier: GPL-2.0-only

package library

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
)

// TestReadDirectoryReportsUnreadableEntriesWithoutFailingTheListing is a #702
// regression. A directory that can be listed but whose entries cannot be
// stat'ed (read permission without search permission) stands in for a
// transient per-entry error on a network mount. The listing must not fail:
// a failed listing drops the directory and its whole subtree from the scan.
func TestReadDirectoryReportsUnreadableEntriesWithoutFailingTheListing(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory search permission")
	}
	directory := filepath.Join(t.TempDir(), "album")
	if err := os.Mkdir(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"01 - a.flac", "02 - b.flac"} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte("audio"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(directory, 0o444); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(directory, 0o755) })

	var reported []string
	entries, err := readDirectoryReporting(directory, func(path string, err error) {
		if err == nil {
			t.Errorf("skip %q reported a nil error", path)
		}
		reported = append(reported, path)
	})
	if err != nil {
		t.Fatalf("readDirectoryReporting error = %v, want the listing to survive unreadable entries", err)
	}
	if len(entries) != 0 {
		t.Fatalf("entries = %#v, want none readable", entries)
	}
	sort.Strings(reported)
	want := []string{filepath.Join(directory, "01 - a.flac"), filepath.Join(directory, "02 - b.flac")}
	if len(reported) != len(want) || reported[0] != want[0] || reported[1] != want[1] {
		t.Fatalf("reported = %v, want %v", reported, want)
	}
}

// TestReadDirectoryFollowsSymbolicLinks ports upstream link handling
// (ManagedFileSystem.GetFileSystemMetadata). A link entry keeps the link
// path and carries the facts of its target. A dangling link and a link loop
// are reported and skipped.
func TestReadDirectoryFollowsSymbolicLinks(t *testing.T) {
	base := t.TempDir()
	directory := filepath.Join(base, "music")
	target := filepath.Join(base, "target")
	for _, path := range []string{directory, target} {
		if err := os.Mkdir(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	targetFile := filepath.Join(base, "track.flac")
	if err := os.WriteFile(targetFile, []byte("linked audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	links := map[string]string{
		"file.flac": targetFile,
		"dir":       target,
		"dangling":  filepath.Join(base, "missing.flac"),
		"loop":      filepath.Join(directory, "loop"),
	}
	for name, destination := range links {
		if err := os.Symlink(destination, filepath.Join(directory, name)); err != nil {
			t.Fatal(err)
		}
	}

	var reported []string
	entries, err := readDirectoryReporting(directory, func(path string, err error) {
		if err == nil {
			t.Errorf("skip %q reported a nil error", path)
		}
		reported = append(reported, path)
	})
	if err != nil {
		t.Fatalf("readDirectoryReporting error = %v", err)
	}
	sort.Strings(reported)
	if want := []string{filepath.Join(directory, "dangling"), filepath.Join(directory, "loop")}; len(reported) != 2 || reported[0] != want[0] || reported[1] != want[1] {
		t.Fatalf("reported = %v, want %v", reported, want)
	}
	byPath := make(map[string]Entry, len(entries))
	for _, entry := range entries {
		byPath[entry.Path] = entry
	}
	if len(byPath) != 2 {
		t.Fatalf("entries = %#v, want the file link and the directory link", entries)
	}
	file, found := byPath[filepath.Join(directory, "file.flac")]
	if !found || file.IsDir || file.Size != int64(len("linked audio")) {
		t.Fatalf("file link entry = %#v (found %t), want a regular file with the target size", file, found)
	}
	dir, found := byPath[filepath.Join(directory, "dir")]
	if !found || !dir.IsDir {
		t.Fatalf("directory link entry = %#v (found %t), want a directory", dir, found)
	}
	if want := directoryIdentity(target); want.Valid && (dir.Identity.Device != want.Device || dir.Identity.Inode != want.Inode) {
		t.Fatalf("directory link identity = %+v, want the target identity %+v", dir.Identity, want)
	}
}

func TestVisitBranchDoesNotShareStorage(t *testing.T) {
	root := visitBranch(nil, FileIdentity{Device: 1, Inode: 1, Valid: true})
	left := visitBranch(root, FileIdentity{Device: 1, Inode: 2, Valid: true})
	right := visitBranch(root, FileIdentity{Device: 1, Inode: 3, Valid: true})
	if onBranch(right, FileIdentity{Device: 1, Inode: 2, Valid: true}) || !onBranch(left, FileIdentity{Device: 1, Inode: 2, Valid: true}) {
		t.Fatalf("left = %v, right = %v, want separate branches", left, right)
	}
	if got := visitBranch(root, FileIdentity{}); len(got) != 1 {
		t.Fatalf("visitBranch with an invalid identity = %v, want the branch unchanged", got)
	}
	if onBranch(root, FileIdentity{Device: 1, Inode: 1}) {
		t.Fatal("onBranch matched an invalid identity")
	}
}

func TestDropIdentityCollisionsKeepsOrdinalLowestPath(t *testing.T) {
	upper, lower, other := "/m/B/One", "/m/B/one", "/m/B/Two"
	desired := []item.Item{
		{ID: dto.UUID{1}, Path: &lower},
		{ID: dto.UUID{2}, Path: &other},
		{ID: dto.UUID{1}, Path: &upper},
	}
	kept, skipped := dropIdentityCollisions(desired)
	if len(kept) != 2 || *kept[0].Path != other || *kept[1].Path != upper {
		t.Fatalf("kept = %v, want [%s %s]", kept, other, upper)
	}
	if len(skipped) != 1 || skipped[0].Path != lower || skipped[0].Status != "skipped" || skipped[0].ItemID != (dto.UUID{1}) {
		t.Fatalf("skipped = %#v, want %s", skipped, lower)
	}
	if kept, skipped := dropIdentityCollisions(desired[:2]); len(kept) != 2 || skipped != nil {
		t.Fatalf("no collision: kept %d skipped %v", len(kept), skipped)
	}
}

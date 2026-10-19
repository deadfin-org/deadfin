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

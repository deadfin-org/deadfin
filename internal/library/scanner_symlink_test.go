// SPDX-License-Identifier: GPL-2.0-only

package library_test

import (
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/naming"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
)

// TestScannerFollowsSymbolicLinks ports upstream link handling. Upstream
// resolves a file link to its target (ManagedFileSystem.GetFileSystemMetadata)
// and enumerates a link to a directory as a directory, so linked media enters
// the library under the link path. A link may point outside the library
// root, as upstream allows. A dangling link is skipped with a warning. A
// directory link to an ancestor is skipped with a warning, and the scan ends.
func TestScannerFollowsSymbolicLinks(t *testing.T) {
	stores, err := sqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("open stores: %v", err)
	}
	t.Cleanup(func() { _ = stores.Close() })
	items := store.NewItemStore(stores)
	base := t.TempDir()
	music := filepath.Join(base, "music")
	outside := filepath.Join(base, "outside")
	for number := 1; number <= 3; number++ {
		writeScannerFile(t, filepath.Join(music, "A", "One", fmt.Sprintf("%02d - t.flac", number)), "audio")
		writeScannerFile(t, filepath.Join(outside, "B", "Two", fmt.Sprintf("%02d - t.flac", number)), "audio")
	}
	writeScannerFile(t, filepath.Join(outside, "loose.flac"), "linked audio")
	symlink(t, filepath.Join(outside, "B"), filepath.Join(music, "B"))
	symlink(t, filepath.Join(outside, "loose.flac"), filepath.Join(music, "A", "One", "04 - t.flac"))
	symlink(t, filepath.Join(outside, "missing.flac"), filepath.Join(music, "A", "One", "05 - t.flac"))
	symlink(t, music, filepath.Join(music, "A", "back"))
	symlink(t, filepath.Join(music, "A"), filepath.Join(music, "A", "One", "up"))

	topology, err := library.NewTopology(t.TempDir(), store.NewLibraryStore(stores))
	if err != nil {
		t.Fatalf("open topology: %v", err)
	}
	t.Cleanup(func() { _ = topology.Close() })
	if _, err := topology.AddVirtualFolder(t.Context(), "Music", "music", library.LibraryOptions{PathInfos: []library.MediaPathInfo{{Path: music}}}); err != nil {
		t.Fatalf("add music library: %v", err)
	}
	var logs bytes.Buffer
	for pass := 1; pass <= 2; pass++ {
		logs.Reset()
		scanner := newScanner(t, topology, items, library.NewDefaultChain(naming.DefaultOptions()), library.ScannerOptions{Concurrency: 2, Logger: slog.New(slog.NewTextHandler(&logs, nil))})
		scannerScan(t, scanner, fmt.Sprintf("scan %d", pass))
		if err := scanner.Close(); err != nil {
			t.Fatalf("close scanner: %v", err)
		}
		audio := make(map[string]item.Item)
		for _, value := range scannerItems(t, items) {
			if value.Kind == item.KindAudio && value.Path != nil {
				audio[*value.Path] = value
			}
		}
		if len(audio) != 7 {
			t.Fatalf("scan %d audio items = %d (%v), want 7", pass, len(audio), audioPaths(audio))
		}
		linkedFile := audio[filepath.Join(music, "A", "One", "04 - t.flac")]
		if linkedFile.Size == nil || *linkedFile.Size != int64(len("linked audio")) {
			t.Fatalf("scan %d file link size = %v, want the target size %d", pass, linkedFile.Size, len("linked audio"))
		}
		if _, found := audio[filepath.Join(music, "B", "Two", "02 - t.flac")]; !found {
			t.Fatalf("scan %d audio paths = %v, want tracks below the directory link path", pass, audioPaths(audio))
		}
		for _, path := range audioPaths(audio) {
			if strings.HasPrefix(path, outside) {
				t.Fatalf("scan %d stored the link target path %q, want the link path", pass, path)
			}
		}
		for _, want := range []string{
			"skip unreadable directory entry",
			filepath.Join(music, "A", "One", "05 - t.flac"),
			"skip directory link to an ancestor",
			filepath.Join(music, "A", "back"),
			filepath.Join(music, "A", "One", "up"),
		} {
			if !strings.Contains(logs.String(), want) {
				t.Fatalf("scan %d logs do not contain %q:\n%s", pass, want, logs.String())
			}
		}
	}
	for _, path := range []string{filepath.Join(music, "A", "back"), filepath.Join(music, "A", "One", "up")} {
		assertScannerNoItemAtPath(t, items, path)
	}
}

func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("Symlink(%q, %q): %v", target, link, err)
	}
}

func audioPaths(audio map[string]item.Item) []string {
	paths := make([]string, 0, len(audio))
	for path := range audio {
		paths = append(paths, path)
	}
	return paths
}

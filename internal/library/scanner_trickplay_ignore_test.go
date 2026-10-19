// SPDX-License-Identifier: GPL-2.0-only

package library_test

import (
	"strings"
	"testing"

	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/naming"
)

// trickplayFixture is a library tree that Jellyfin scanned with
// SaveTrickplayWithMedia on: every video has a sibling "<name>.trickplay"
// directory with "<width> - <tiles>" tile directories.
type trickplayFixture struct {
	harness  *scannerHarness
	media    []string
	trickDir []string
	tileDir  []string
}

func newTrickplayFixture(t *testing.T) *trickplayFixture {
	t.Helper()
	harness := newScannerHarness(t, &scannerFixtureResolver{})
	if err := harness.scanner.Close(); err != nil {
		t.Fatalf("close fixture scanner: %v", err)
	}
	harness.chain = library.NewDefaultChain(naming.DefaultOptions())
	harness.scanner = newScanner(t, harness.topology, harness.items, harness.chain, library.ScannerOptions{Concurrency: 1, ReadDir: scannerReadDir})

	movieDirectory := scannerPath(t, harness.source, "Feature (2023)")
	clipsDirectory := scannerPath(t, harness.source, "Clips")
	fixture := &trickplayFixture{
		harness: harness,
		media: []string{
			scannerPath(t, movieDirectory, "Feature (2023) WEBDL-2160p.mkv"),
			scannerPath(t, clipsDirectory, "Clip One.mkv"),
		},
		trickDir: []string{
			scannerPath(t, movieDirectory, "Feature (2023) WEBDL-2160p.trickplay"),
			// The upstream glob matches case-insensitively.
			scannerPath(t, clipsDirectory, "Clip One.TrickPlay"),
		},
	}
	for _, directory := range fixture.trickDir {
		fixture.tileDir = append(fixture.tileDir, scannerPath(t, directory, "320 - 10x10"))
	}
	for _, media := range fixture.media {
		writeScannerFile(t, media, "media")
	}
	return fixture
}

func (f *trickplayFixture) writeTrickplay(t *testing.T) {
	t.Helper()
	for _, directory := range f.tileDir {
		writeScannerFile(t, scannerPath(t, directory, "0.jpg"), "tile")
	}
}

func (f *trickplayFixture) scan(t *testing.T) {
	t.Helper()
	result := scannerScan(t, f.harness.scanner, "scan trickplay fixture")
	if len(result.Errors) != 0 {
		t.Fatalf("scan trickplay fixture errors = %v, want none", result.Errors)
	}
}

func (f *trickplayFixture) assertNoTrickplayItems(t *testing.T) {
	t.Helper()
	for _, candidate := range scannerItems(t, f.harness.items) {
		if candidate.Path != nil && strings.Contains(strings.ToLower(*candidate.Path), ".trickplay") {
			t.Fatalf("scanner indexed trickplay path %q as %s", *candidate.Path, candidate.Kind)
		}
	}
	for _, media := range f.media {
		if found := scannerItemAtPath(t, f.harness.items, media); found.IsFolder {
			t.Fatalf("media %q resolved as folder %s, want a video item", media, found.Kind)
		}
	}
}

// TestScannerIgnoresJellyfinTrickplayDirectories ports the trickplay patterns
// of upstream IgnorePatterns (IgnorePatterns.cs:72-74). Issue #704: a library
// that Jellyfin scanned with trickplay saved next to the media must not
// produce Folder items for the "<name>.trickplay" directories or their tile
// directories.
func TestScannerIgnoresJellyfinTrickplayDirectories(t *testing.T) {
	fixture := newTrickplayFixture(t)
	fixture.writeTrickplay(t)

	fixture.scan(t)
	fixture.assertNoTrickplayItems(t)
}

// TestScannerRemovesStoredTrickplayFolders defends the upgrade path: a
// database that an earlier scanner filled with trickplay Folder items loses
// them on the next scan.
func TestScannerRemovesStoredTrickplayFolders(t *testing.T) {
	fixture := newTrickplayFixture(t)
	fixture.scan(t)

	for index, directory := range fixture.trickDir {
		media := scannerItemAtPath(t, fixture.harness.items, fixture.media[index])
		parent, err := fixture.harness.items.Get(t.Context(), *media.ParentID)
		if err != nil {
			t.Fatalf("load parent of %q: %v", fixture.media[index], err)
		}
		stale := trickplayFolder(directory, parent)
		if err := fixture.harness.items.Upsert(t.Context(), stale); err != nil {
			t.Fatalf("seed stale trickplay folder %q: %v", directory, err)
		}
		tiles := trickplayFolder(fixture.tileDir[index], stale)
		if err := fixture.harness.items.Upsert(t.Context(), tiles); err != nil {
			t.Fatalf("seed stale trickplay tile folder %q: %v", fixture.tileDir[index], err)
		}
		scannerItemAtPath(t, fixture.harness.items, fixture.tileDir[index])
	}
	fixture.writeTrickplay(t)

	fixture.scan(t)
	fixture.assertNoTrickplayItems(t)
}

func trickplayFolder(path string, parent item.Item) item.Item {
	name := path[strings.LastIndexAny(path, `/\`)+1:]
	parentID := parent.ID
	top := parent.ID
	if parent.TopParentID != nil {
		top = *parent.TopParentID
	}
	return item.Item{
		ID:          library.ItemID(item.KindFolder, path, ""),
		Kind:        item.KindFolder,
		Path:        &path,
		Name:        &name,
		IsFolder:    true,
		ParentID:    &parentID,
		TopParentID: &top,
	}
}

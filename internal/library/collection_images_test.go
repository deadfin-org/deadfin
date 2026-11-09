// SPDX-License-Identifier: GPL-2.0-only

package library_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/naming"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
)

// collectionImageFake records CollectionFolder image requests and stores a
// generated image path as the provider does.
type collectionImageFake struct {
	calls   int
	sources [][]item.Item
	now     func() time.Time
}

func (f *collectionImageFake) RefreshDynamicImages(context.Context, library.DynamicImageInput, *item.Item) error {
	return nil
}

func (f *collectionImageFake) CreateCollectionFolderImage(_ context.Context, target *item.Item, itemsWithImages []item.Item) (bool, error) {
	f.calls++
	f.sources = append(f.sources, itemsWithImages)
	modified := f.now()
	target.ImageInfos = []item.ImageInfo{{Type: item.ImageTypePrimary, Path: filepath.ToSlash(library.InternalMetadataDir("%MetadataPath%", target.ID)) + "/poster.png", DateModified: &modified, Width: 960, Height: 540}}
	return true, nil
}

func (f *collectionImageFake) IsDynamicImage(target item.Item, image item.ImageInfo) bool {
	return strings.HasPrefix(image.Path, filepath.ToSlash(library.InternalMetadataDir("%MetadataPath%", target.ID))+"/")
}

// TestScanCreatesCollectionFolderImage covers CollectionFolderImageProvider:
// a scan gives a library without a Primary image a collage of up to eight of
// its items with images, keeps it for seven days, and then creates it again.
func TestScanCreatesCollectionFolderImage(t *testing.T) {
	stores, err := sqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("open stores: %v", err)
	}
	t.Cleanup(func() { _ = stores.Close() })
	root := t.TempDir()
	topology, err := library.NewTopology(root, store.NewLibraryStore(stores))
	if err != nil {
		t.Fatalf("open topology: %v", err)
	}
	t.Cleanup(func() { _ = topology.Close() })
	media := filepath.Join(t.TempDir(), "movies")
	for _, name := range []string{"Alpha (1999)", "Bravo (2003)", "Charlie (2003)"} {
		if err := os.MkdirAll(filepath.Join(media, name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(media, name, name+".mkv"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	options := library.DefaultLibraryOptions()
	options.PathInfos = []library.MediaPathInfo{{Path: media}}
	folder, err := topology.AddVirtualFolder(t.Context(), "Movies", "movies", options)
	if err != nil {
		t.Fatalf("add library: %v", err)
	}
	items := store.NewItemStore(stores)
	if err := items.BootstrapSystemFolders(t.Context(), root); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	fake := clock.NewFake(time.Date(2026, time.November, 9, 12, 0, 0, 0, time.UTC))
	creator := &collectionImageFake{now: fake.Now}
	scanner, err := library.NewScanner(topology, items, library.NewDefaultChain(naming.DefaultOptions()), library.ScannerOptions{Clock: fake, DynamicImages: creator})
	if err != nil {
		t.Fatalf("new scanner: %v", err)
	}
	t.Cleanup(func() { _ = scanner.Close() })
	scan := func() {
		t.Helper()
		if result, err := scanner.Scan(t.Context()); err != nil || len(result.Errors) != 0 {
			t.Fatalf("scan = %v, errors %v", err, result.Errors)
		}
	}

	// No item has an image: GetItemsWithImages is empty, so no image.
	scan()
	if creator.calls != 0 {
		t.Fatalf("collage requests without item images = %d, want 0", creator.calls)
	}

	// An image in the internal metadata directory carries through scans.
	movies, err := items.Query(t.Context(), store.ItemQuery{IncludeItemTypes: []item.Kind{item.KindMovie}, Recursive: true})
	if err != nil || len(movies.Items) != 3 {
		t.Fatalf("movies = %d, %v", len(movies.Items), err)
	}
	poster := movies.Items[0]
	poster.ImageInfos = []item.ImageInfo{{Type: item.ImageTypePrimary, Path: filepath.ToSlash(library.InternalMetadataDir("%MetadataPath%", poster.ID)) + "/poster.jpg", Width: 200, Height: 300}}
	if err := items.EditItems(t.Context(), []item.Item{poster}, nil); err != nil {
		t.Fatalf("store poster: %v", err)
	}
	scan()
	if creator.calls != 1 || len(creator.sources[0]) != 1 || creator.sources[0][0].ID != poster.ID {
		t.Fatalf("collage requests = %d with sources %v, want one with the movie that has a poster", creator.calls, creator.sources)
	}
	assertCollectionPrimary(t, items, folder.CollectionFolderID)

	fake.Advance(6 * 24 * time.Hour)
	scan()
	if creator.calls != 1 {
		t.Fatalf("collage requests within seven days = %d, want 1", creator.calls)
	}
	fake.Advance(2 * 24 * time.Hour)
	scan()
	if creator.calls != 2 {
		t.Fatalf("collage requests after seven days = %d, want 2", creator.calls)
	}
}

func assertCollectionPrimary(t *testing.T, items *store.ItemStore, id dto.UUID) {
	t.Helper()
	loaded, err := items.GetBatch(t.Context(), []dto.UUID{id})
	if err != nil || len(loaded) != 1 {
		t.Fatalf("load collection folder: %d, %v", len(loaded), err)
	}
	for _, image := range loaded[0].ImageInfos {
		if image.Type == item.ImageTypePrimary {
			return
		}
	}
	t.Fatalf("collection folder images = %+v, want a Primary image", loaded[0].ImageInfos)
}

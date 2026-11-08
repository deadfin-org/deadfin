// SPDX-License-Identifier: GPL-2.0-only

package itemedit_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/itemedit"
	"github.com/deadfin-org/deadfin/internal/path"
	"github.com/deadfin-org/deadfin/internal/store"
)

type imageFixture struct {
	items    *store.ItemStore
	service  *itemedit.Service
	metadata string
	paths    path.VirtualPathExpander
}

func newImageFixture(t *testing.T) imageFixture {
	t.Helper()
	base := newFixture(t, nil)
	metadata := filepath.Join(t.TempDir(), "metadata")
	paths := path.NewVirtualPathExpander(filepath.Join(t.TempDir(), "data"), metadata)
	service := itemedit.New(itemedit.Options{Items: base.items, Clock: clock.NewFake(editNow()), MetadataPath: metadata, Paths: paths})
	return imageFixture{items: base.items, service: service, metadata: metadata, paths: paths}
}

func imagesOf(t *testing.T, items *store.ItemStore, itemID dto.UUID, imageType item.ImageType) []item.ImageInfo {
	t.Helper()
	var result []item.ImageInfo
	for _, image := range mustGet(t, items, itemID).ImageInfos {
		if image.Type == imageType {
			result = append(result, image)
		}
	}
	return result
}

func TestSetImageSavesToInternalMetadataPathAndReplacesPrevious(t *testing.T) {
	f := newImageFixture(t)
	movie := id(30)
	mustUpsert(t, f.items, item.Item{ID: movie, Kind: item.KindMovie, Name: text("Movie"), Path: text("/media/movies/m.mkv")})

	if err := f.service.SetImage(t.Context(), movie, item.ImageTypePrimary, "image/jpeg", strings.NewReader("first")); err != nil {
		t.Fatalf("SetImage: %v", err)
	}
	primary := imagesOf(t, f.items, movie, item.ImageTypePrimary)
	if len(primary) != 1 || primary[0].Index != 0 {
		t.Fatalf("primary images = %+v; want one at index 0", primary)
	}
	hex := movie.String()
	if !strings.HasPrefix(primary[0].Path, "%MetadataPath%/library/"+hex[:2]+"/"+hex+"/poster-") || !strings.HasSuffix(primary[0].Path, ".jpg") {
		t.Fatalf("stored path = %q; want %%MetadataPath%%/library/%s/%s/poster-<tag>.jpg", primary[0].Path, hex[:2], hex)
	}
	first := f.paths.Expand(primary[0].Path)
	if data, err := os.ReadFile(first); err != nil || string(data) != "first" {
		t.Fatalf("saved file = %q, %v", data, err)
	}

	if err := f.service.SetImage(t.Context(), movie, item.ImageTypePrimary, "image/png", strings.NewReader("second")); err != nil {
		t.Fatalf("SetImage replace: %v", err)
	}
	primary = imagesOf(t, f.items, movie, item.ImageTypePrimary)
	if len(primary) != 1 || !strings.HasSuffix(primary[0].Path, ".png") {
		t.Fatalf("replaced primary = %+v", primary)
	}
	if _, err := os.Stat(first); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("previous internal image still exists: %v", err)
	}
}

func TestBackdropAppendDeleteAndReorder(t *testing.T) {
	f := newImageFixture(t)
	movie := id(31)
	mustUpsert(t, f.items, item.Item{ID: movie, Kind: item.KindMovie, Name: text("Movie"), Path: text("/media/movies/m.mkv"),
		ImageInfos: []item.ImageInfo{{Type: item.ImageTypeBackdrop, Index: 0, Path: "https://image.tmdb.org/t/p/original/remote.jpg"}}})

	for _, content := range []string{"one", "two"} {
		if err := f.service.SetImage(t.Context(), movie, item.ImageTypeBackdrop, "image/jpeg", strings.NewReader(content)); err != nil {
			t.Fatalf("SetImage backdrop: %v", err)
		}
	}
	backdrops := imagesOf(t, f.items, movie, item.ImageTypeBackdrop)
	if len(backdrops) != 3 || backdrops[1].Index != 1 || backdrops[2].Index != 2 {
		t.Fatalf("backdrops = %+v; want remote then two appended", backdrops)
	}
	if !strings.Contains(backdrops[1].Path, "/backdrop1-") || !strings.Contains(backdrops[2].Path, "/backdrop2-") {
		t.Fatalf("backdrop names = %q, %q", backdrops[1].Path, backdrops[2].Path)
	}
	one, two := backdrops[1].Path, backdrops[2].Path

	// Remote images do not move (SwapImagesAsync needs two local files).
	if err := f.service.UpdateImageIndex(t.Context(), movie, item.ImageTypeBackdrop, 0, 1); err != nil {
		t.Fatalf("UpdateImageIndex remote: %v", err)
	}
	if got := imagesOf(t, f.items, movie, item.ImageTypeBackdrop); got[1].Path != one {
		t.Fatalf("remote swap moved images: %+v", got)
	}
	if err := f.service.UpdateImageIndex(t.Context(), movie, item.ImageTypeBackdrop, 1, 2); err != nil {
		t.Fatalf("UpdateImageIndex: %v", err)
	}
	if got := imagesOf(t, f.items, movie, item.ImageTypeBackdrop); got[1].Path != two || got[2].Path != one {
		t.Fatalf("swapped backdrops = %+v", got)
	}
	if err := f.service.UpdateImageIndex(t.Context(), movie, item.ImageTypePrimary, 0, 1); !errors.Is(err, itemedit.ErrInvalidRequest) {
		t.Fatalf("UpdateImageIndex(Primary) = %v; want ErrInvalidRequest", err)
	}

	// Deleting index 1 removes its file and closes the gap.
	if err := f.service.DeleteImage(t.Context(), movie, item.ImageTypeBackdrop, 1); err != nil {
		t.Fatalf("DeleteImage: %v", err)
	}
	if _, err := os.Stat(f.paths.Expand(two)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted backdrop file still exists: %v", err)
	}
	backdrops = imagesOf(t, f.items, movie, item.ImageTypeBackdrop)
	if len(backdrops) != 2 || backdrops[1].Index != 1 || backdrops[1].Path != one {
		t.Fatalf("backdrops after delete = %+v", backdrops)
	}
	// A missing index is a no-op; a remote image is removed without a file.
	if err := f.service.DeleteImage(t.Context(), movie, item.ImageTypeBackdrop, 9); err != nil {
		t.Fatalf("DeleteImage(missing index): %v", err)
	}
	if err := f.service.DeleteImage(t.Context(), movie, item.ImageTypeBackdrop, 0); err != nil {
		t.Fatalf("DeleteImage(remote): %v", err)
	}
	if backdrops = imagesOf(t, f.items, movie, item.ImageTypeBackdrop); len(backdrops) != 1 || backdrops[0].Index != 0 || backdrops[0].Path != one {
		t.Fatalf("backdrops after remote delete = %+v", backdrops)
	}
}

func TestDeleteImageRemovesLocalArtworkFile(t *testing.T) {
	f := newImageFixture(t)
	directory := t.TempDir()
	poster := filepath.Join(directory, "poster.jpg")
	if err := os.WriteFile(poster, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	movie := id(32)
	mustUpsert(t, f.items, item.Item{ID: movie, Kind: item.KindMovie, Name: text("Movie"), Path: text(filepath.Join(directory, "m.mkv")),
		ImageInfos: []item.ImageInfo{{Type: item.ImageTypePrimary, Index: 0, Path: poster}}})
	if err := f.service.DeleteImage(t.Context(), movie, item.ImageTypePrimary, 0); err != nil {
		t.Fatalf("DeleteImage: %v", err)
	}
	if _, err := os.Stat(poster); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("local artwork still exists: %v", err)
	}
	if got := imagesOf(t, f.items, movie, item.ImageTypePrimary); len(got) != 0 {
		t.Fatalf("primary after delete = %+v", got)
	}
}

func TestSetImageRejectsUnknownMediaTypeAndMissingItem(t *testing.T) {
	f := newImageFixture(t)
	if err := f.service.SetImage(t.Context(), id(98), item.ImageTypePrimary, "image/png", bytes.NewReader(nil)); !errors.Is(err, itemedit.ErrItemNotFound) {
		t.Fatalf("SetImage(missing) = %v; want ErrItemNotFound", err)
	}
	movie := id(33)
	mustUpsert(t, f.items, item.Item{ID: movie, Kind: item.KindMovie, Name: text("Movie"), Path: text("/media/movies/m.mkv")})
	if err := f.service.SetImage(t.Context(), movie, item.ImageTypePrimary, "", nil); !errors.Is(err, itemedit.ErrUnsupportedImage) {
		t.Fatalf("SetImage(no media type) = %v; want ErrUnsupportedImage", err)
	}
}

func TestRemoveRemoteImageMatchesPathAndReindexes(t *testing.T) {
	f := newImageFixture(t)
	movie := id(34)
	const gone = "https://cdn.example/gone.jpg"
	mustUpsert(t, f.items, item.Item{ID: movie, Kind: item.KindMovie, Name: text("Movie"), Path: text("/media/movies/m.mkv"),
		ImageInfos: []item.ImageInfo{
			{Type: item.ImageTypeBackdrop, Index: 0, Path: gone},
			{Type: item.ImageTypeBackdrop, Index: 1, Path: "https://cdn.example/kept.jpg"},
		}})
	for range 2 {
		if err := f.service.RemoveRemoteImage(t.Context(), movie, item.ImageTypeBackdrop, gone); err != nil {
			t.Fatalf("RemoveRemoteImage: %v", err)
		}
	}
	got := imagesOf(t, f.items, movie, item.ImageTypeBackdrop)
	if len(got) != 1 || got[0].Path != "https://cdn.example/kept.jpg" || got[0].Index != 0 {
		t.Fatalf("backdrops after two removals = %+v; want only the kept image at index 0", got)
	}
}

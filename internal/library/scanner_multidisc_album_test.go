// SPDX-License-Identifier: GPL-2.0-only

package library_test

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/dtobuild"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/naming"
	"github.com/deadfin-org/deadfin/internal/store"
)

// multiDiscProbe returns the tags of the #703 report: each track carries
// its album and album artist, and the album folder adds a "(year)".
type multiDiscProbe struct{}

func (multiDiscProbe) Probe(_ context.Context, input library.MediaProbeInput) (library.MediaProbeResult, error) {
	if input.Item.Kind != item.KindAudio {
		return library.MediaProbeResult{}, nil
	}
	folder := filepath.Dir(*input.Item.Path)
	if naming.IsMultiDiscAlbum(folder, naming.DefaultOptions()) {
		folder = filepath.Dir(folder)
	}
	album := strings.TrimSpace(strings.Split(filepath.Base(folder), "(")[0])
	artist := filepath.Base(filepath.Dir(folder))
	return library.MediaProbeResult{Album: album, Artists: []string{artist}, AlbumArtists: []string{artist}}, nil
}

// TestScanMultiDiscAlbumIsOneAlbum covers #703. Jellyfin makes one
// MusicAlbum of an album folder whose tracks are in disc folders ("CD 01",
// "Disc 1", "Digital Media 01"); the disc folders are plain folders, and
// the album's parent is the artist. A library scanned before the fix holds
// one MusicAlbum per disc folder below a MusicArtist named after the album;
// a rescan replaces them.
func TestScanMultiDiscAlbumIsOneAlbum(t *testing.T) {
	h := newScannerHarness(t, &scannerFixtureResolver{})
	rewriteScannerCollection(t, h.topology, "movies", "music")
	tracks := map[string][]string{
		filepath.Join("Beyoncé", "I Am… Sasha Fierce (2008)"):          {"CD 01/01 - If I Were a Boy.flac", "CD 01/02 - Halo.flac", "CD 02/01 - Single Ladies.flac"},
		filepath.Join("Jimmy Eat World", "Futures (2004)"):             {"Digital Media 01/01 - Futures.flac", "Digital Media 02/01 - Pain.flac", "Digital Media 03/01 - Demo.flac"},
		filepath.Join("Classical", "Lieder (1990)"):                    {"Disc 1/01 - Erlkönig.flac", "Disc 2/01 - Der Wanderer.flac"},
		filepath.Join("Plain Artist", "Flat Album (2001)"):             {"01 - Track.flac"},
		filepath.Join("Plain Artist", "Box Set (2010)", "Disc 1 Live"): {"01 - Intro.flac"},
	}
	for album, files := range tracks {
		for _, file := range files {
			writeScannerFile(t, filepath.Join(h.source, album, filepath.FromSlash(file)), "audio")
		}
	}

	scan := func(prefixes []string) {
		t.Helper()
		if err := h.scanner.Close(); err != nil {
			t.Fatalf("close scanner: %v", err)
		}
		options := naming.DefaultOptions()
		options.AlbumStackingPrefixes = prefixes
		h.chain = library.NewDefaultChain(options)
		h.scanner = newScanner(t, h.topology, h.items, h.chain, library.ScannerOptions{Concurrency: 2, MediaProbe: multiDiscProbe{}})
		result, err := h.scanner.Scan(t.Context())
		if err != nil || len(result.Errors) != 0 {
			t.Fatalf("scan = %v, %v", result.Errors, err)
		}
	}
	// Without album stacking prefixes the resolvers behave as before the
	// fix, which leaves the per-disc albums of the report.
	scan(nil)
	if got := kindNames(t, h.items, item.KindMusicAlbum); !slices.Contains(got, "CD 01") || !slices.Contains(got, "Digital Media 03") {
		t.Fatalf("pre-fix albums = %v, want per-disc albums", got)
	}

	scan(naming.DefaultOptions().AlbumStackingPrefixes)
	wantAlbums := []string{"Box Set (2010)", "Flat Album (2001)", "Futures (2004)", "I Am… Sasha Fierce (2008)", "Lieder (1990)"}
	if got := kindNames(t, h.items, item.KindMusicAlbum); !slices.Equal(got, wantAlbums) {
		t.Fatalf("albums after rescan = %v, want %v", got, wantAlbums)
	}
	wantArtists := []string{"Beyoncé", "Classical", "Jimmy Eat World", "Plain Artist"}
	if got := kindNames(t, h.items, item.KindMusicArtist); !slices.Equal(got, wantArtists) {
		t.Fatalf("artists after rescan = %v, want %v", got, wantArtists)
	}

	album := scannerItemAtPath(t, h.items, filepath.Join(h.source, "Beyoncé", "I Am… Sasha Fierce (2008)"))
	artist := scannerItemAtPath(t, h.items, filepath.Join(h.source, "Beyoncé"))
	if album.Kind != item.KindMusicAlbum || album.ParentID == nil || *album.ParentID != artist.ID || artist.Kind != item.KindMusicArtist {
		t.Fatalf("album %s parent = %v, want MusicArtist %s", album.Kind, album.ParentID, artist.ID)
	}
	for _, disc := range []string{"CD 01", "CD 02"} {
		folder := scannerItemAtPath(t, h.items, filepath.Join(h.source, "Beyoncé", "I Am… Sasha Fierce (2008)", disc))
		if folder.Kind != item.KindFolder || folder.ParentID == nil || *folder.ParentID != album.ID {
			t.Fatalf("disc folder %s = %s below %v, want Folder below album %s", disc, folder.Kind, folder.ParentID, album.ID)
		}
	}

	// A user's album query lists the tracks of every disc, and each track
	// projects the album as its AlbumId with the tagged album artist.
	user := dto.UUID{0x70, 0x3}
	page, err := h.items.Query(t.Context(), store.ItemQuery{ParentID: &album.ID, UserID: &user, SortBy: []store.ItemSortBy{store.ItemSortBySortName}, SortOrder: []store.SortOrder{store.SortOrderAscending}})
	if err != nil {
		t.Fatalf("query album tracks: %v", err)
	}
	if got := len(page.Items); got != 3 {
		t.Fatalf("album tracks = %d, want 3 across both discs", got)
	}
	enrichments, err := dtobuild.NewStoreEnricher(h.items, nil, nil).Enrich(t.Context(), append(page.Items, album), store.ItemQueryDTOOptions{Fields: []string{"ChildCount"}}, nil)
	if err != nil {
		t.Fatalf("enrich album tracks: %v", err)
	}
	for _, track := range page.Items {
		got := enrichments[track.ID]
		if got.AlbumID == nil || *got.AlbumID != album.ID || got.Album != "I Am… Sasha Fierce (2008)" {
			t.Fatalf("track %s album = %v %q, want %s", *track.Path, got.AlbumID, got.Album, album.ID)
		}
		if len(got.AlbumArtists) != 1 || got.AlbumArtists[0].Name != "Beyoncé" {
			t.Fatalf("track %s album artists = %#v, want Beyoncé", *track.Path, got.AlbumArtists)
		}
	}
	if got := enrichments[album.ID].ChildCount; got == nil || *got != 3 {
		t.Fatalf("album ChildCount = %v, want 3 tracks", got)
	}
}

func kindNames(t *testing.T, items *store.ItemStore, kind item.Kind) []string {
	t.Helper()
	var names []string
	for _, value := range scannerItems(t, items) {
		if value.Kind == kind && value.Name != nil {
			names = append(names, *value.Name)
		}
	}
	slices.Sort(names)
	return names
}

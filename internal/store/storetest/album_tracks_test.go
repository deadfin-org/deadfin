// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
	storetestpkg "github.com/deadfin-org/deadfin/internal/store/storetest"
)

// TestItemStoreAlbumTracksSQLite proves the MusicAlbum ParentId rewrite and
// the album track counts against SQLite.
func TestItemStoreAlbumTracksSQLite(t *testing.T) {
	stores, err := sqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("open SQLite: %v", err)
	}
	t.Cleanup(func() { _ = stores.Close() })
	runItemStoreAlbumTracks(t, stores)
}

// TestItemStoreAlbumTracksPostgres runs the same cases against PostgreSQL
// when DEADFIN_TEST_POSTGRES_DSN is configured.
func TestItemStoreAlbumTracksPostgres(t *testing.T) {
	runItemStoreAlbumTracks(t, storetestpkg.OpenPostgres(t))
}

// runItemStoreAlbumTracks ports Folder.GetItemsInternal for a MusicAlbum
// (jellyfin@b04614e Folder.cs:1016-1066, 1460-1485; MusicAlbum.cs:67, 89-92):
// a user's direct query of an album lists its Tracks, the recursive Audio
// children, so a multi-disc album lists the tracks of every disc folder.
func runItemStoreAlbumTracks(t *testing.T, stores store.Stores) {
	t.Helper()
	items := store.NewItemStore(stores)
	id := func(value int) dto.UUID {
		t.Helper()
		parsed, err := dto.ParseUUID(fmt.Sprintf("%032x", value))
		if err != nil {
			t.Fatalf("parse ID %d: %v", value, err)
		}
		return parsed
	}
	intp := func(value int) *int { return &value }
	named := func(value int, kind item.Kind, parent dto.UUID, name string) item.Item {
		return item.Item{ID: id(value), Kind: kind, ParentID: &parent, Name: &name, SortName: &name, IsFolder: kind != item.KindAudio}
	}
	library := item.Item{ID: id(1), Kind: item.KindCollectionFolder, IsFolder: true}
	artist := named(2, item.KindMusicArtist, library.ID, "Artist")
	album := named(10, item.KindMusicAlbum, artist.ID, "Album")
	discOne := named(11, item.KindFolder, album.ID, "CD 01")
	discTwo := named(12, item.KindFolder, album.ID, "CD 02")
	track := func(value int, parent dto.UUID, disc, index int, name string) item.Item {
		t := named(value, item.KindAudio, parent, name)
		t.ParentIndexNumber, t.IndexNumber = intp(disc), intp(index)
		return t
	}
	oneA := track(21, discOne.ID, 1, 1, "01 a")
	oneB := track(22, discOne.ID, 1, 2, "02 b")
	twoA := track(23, discTwo.ID, 2, 1, "03 c")
	flat := named(30, item.KindMusicAlbum, artist.ID, "Flat")
	flatTrack := track(31, flat.ID, 0, 1, "04 d")
	for _, value := range []item.Item{library, artist, album, discOne, discTwo, oneA, oneB, twoA, flat, flatTrack} {
		if err := items.Upsert(t.Context(), value); err != nil {
			t.Fatalf("upsert %s: %v", value.ID, err)
		}
	}

	user := id(99)
	sorted := func(query store.ItemQuery) store.ItemQuery {
		query.SortBy = []store.ItemSortBy{store.ItemSortBySortName}
		query.SortOrder = []store.SortOrder{store.SortOrderAscending}
		query.EnableTotalRecordCount = true
		return query
	}
	albumTracks := []dto.UUID{oneA.ID, oneB.ID, twoA.ID}
	displayFolders := true
	for _, tc := range []struct {
		name  string
		query store.ItemQuery
		want  []dto.UUID
	}{
		{name: "user parent query of a multi-disc album lists its tracks", query: store.ItemQuery{ParentID: &album.ID, UserID: &user}, want: albumTracks},
		{name: "user parent query with the Audio type lists its tracks", query: store.ItemQuery{ParentID: &album.ID, UserID: &user, IncludeItemTypes: []item.Kind{item.KindAudio, item.KindFolder}}, want: albumTracks},
		{name: "user parent query without the Audio type lists nothing", query: store.ItemQuery{ParentID: &album.ID, UserID: &user, IncludeItemTypes: []item.Kind{item.KindFolder}}, want: []dto.UUID{}},
		{name: "user parent query of a flat album lists its tracks", query: store.ItemQuery{ParentID: &flat.ID, UserID: &user}, want: []dto.UUID{flatTrack.ID}},
		{name: "DisplayAlbumFolders keeps the disc folders", query: store.ItemQuery{ParentID: &album.ID, UserID: &user, DisplayAlbumFolders: &displayFolders}, want: []dto.UUID{discOne.ID, discTwo.ID}},
		{name: "parent query without a user keeps the folder children", query: store.ItemQuery{ParentID: &album.ID}, want: []dto.UUID{discOne.ID, discTwo.ID}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := items.Query(t.Context(), sorted(tc.query))
			if err != nil {
				t.Fatalf("query: %v", err)
			}
			ids := make([]dto.UUID, len(got.Items))
			for index, value := range got.Items {
				ids[index] = value.ID
			}
			if !reflect.DeepEqual(ids, tc.want) || got.TotalRecordCount != len(tc.want) {
				t.Fatalf("IDs/count = %v/%d, want %v/%d", ids, got.TotalRecordCount, tc.want, len(tc.want))
			}
		})
	}

	projections, err := items.DTOProjections(t.Context(), []dto.UUID{album.ID, flat.ID, artist.ID}, nil, false, false, true, true, nil)
	if err != nil {
		t.Fatalf("projections: %v", err)
	}
	for _, tc := range []struct {
		id         dto.UUID
		child, all int
	}{
		{id: album.ID, child: 3, all: 3},
		{id: flat.ID, child: 1, all: 1},
		{id: artist.ID, child: 2, all: 8},
	} {
		if got := projections.ChildCounts[tc.id]; got != tc.child {
			t.Fatalf("ChildCounts[%s] = %d, want %d", tc.id, got, tc.child)
		}
		if got := projections.RecursiveCounts[tc.id]; got != tc.all {
			t.Fatalf("RecursiveCounts[%s] = %d, want %d", tc.id, got, tc.all)
		}
	}
}

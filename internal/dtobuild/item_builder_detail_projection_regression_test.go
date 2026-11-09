// SPDX-License-Identifier: GPL-2.0-only

package dtobuild_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/dtobuild"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
)

// TestBuilderDetailedItemFieldsKeepStockEmptyFactsAndKindIdentities verifies
// the materialization half of direct item detail. A caller can request the
// stock detail field set without turning absent collections into omissions;
// conversely unrelated sparse-only fields must remain absent. This deliberately
// covers a movie and a generic Folder because their display preference identity
// rules are different upstream.
func TestBuilderDetailedItemFieldsKeepStockEmptyFactsAndKindIdentities(t *testing.T) {
	movieID, folderID, userID := builderUUID(t, 170), builderUUID(t, 171), builderUUID(t, 172)
	name, folderName, path := "Detailed movie", "Detailed folder", "/library/detailed.mkv"
	created := time.Date(2026, time.July, 27, 17, 55, 42, 0, time.UTC)
	width, height, childCount, recursiveCount := 320, 180, 0, 1
	movie := item.Item{
		ID: movieID, Kind: item.KindMovie, IsMovie: true, ParentID: &folderID, Name: &name, Path: &path,
		DateCreated: &created, Width: &width, Height: &height, Values: item.ItemValues{Genres: []string{}, Tags: []string{}},
	}
	folder := item.Item{ID: folderID, Kind: item.KindFolder, IsFolder: true, Name: &folderName, DateCreated: &created}
	builder := dtobuild.NewBuilder(nil, projectionEnricher{values: map[dto.UUID]dtobuild.Enrichment{
		movieID: {
			MediaSources:       []dto.MediaSourceInfo{},
			MediaStreams:       []dto.MediaStreamInfo{},
			People:             []dto.BaseItemPerson{},
			ProviderIDs:        map[string]string{},
			UserData:           &dto.UserItemDataDto{ItemID: movieID, Key: movieID.String()},
			ChildCount:         &childCount,
			RecursiveItemCount: &recursiveCount,
		},
		folderID: {
			People:             []dto.BaseItemPerson{},
			ProviderIDs:        map[string]string{},
			UserData:           &dto.UserItemDataDto{ItemID: folderID, Key: folderID.String()},
			ChildCount:         &childCount,
			RecursiveItemCount: &recursiveCount,
		},
	}})
	result, err := builder.BuildDetailed(t.Context(), []item.Item{movie, folder}, store.ItemQueryDTOOptions{Fields: []string{"Path", "MediaSources", "MediaStreams"}, EnableUserData: true}, &userID)
	if err != nil {
		t.Fatalf("build stock detailed item projection: %v", err)
	}
	if got, want := len(result.Items), 2; got != want {
		t.Fatalf("detailed projection item count = %d, want %d", got, want)
	}
	for _, projected := range result.Items {
		wire := builderWireObject(t, projected)
		isMovie := projected.ID == movieID
		builderAssertDetailedStockWire(t, wire, projected.ID, isMovie)
	}
}

func builderAssertDetailedStockWire(t *testing.T, wire map[string]json.RawMessage, id dto.UUID, movie bool) {
	t.Helper()
	for _, field := range []string{
		"CanDelete", "CanDownload", "Chapters", "DateCreated", "DisplayPreferencesId", "EnableMediaSourceDisplay", "ExternalUrls", "GenreItems", "Genres", "LocalTrailerCount", "LockData", "LockedFields", "People", "PlayAccess", "ProductionLocations", "ProviderIds", "RemoteTrailers", "SpecialFeatureCount", "Studios", "Taglines", "Tags", "Trickplay", "UserData",
	} {
		if _, found := wire[field]; !found {
			t.Errorf("detailed item %s omitted stock field %s", id, field)
		}
	}
	if movie {
		for _, field := range []string{"Path", "MediaSources", "MediaStreams"} {
			if _, found := wire[field]; !found {
				t.Errorf("detailed movie %s omitted requested %s", id, field)
			}
		}
	}
	for field, want := range map[string]string{
		"Chapters": "[]", "ExternalUrls": "[]", "GenreItems": "[]", "Genres": "[]", "LockedFields": "[]", "People": "[]", "ProductionLocations": "[]", "ProviderIds": "{}", "RemoteTrailers": "[]", "Studios": "[]", "Taglines": "[]", "Tags": "[]", "Trickplay": "{}",
	} {
		if got := string(wire[field]); got != want {
			t.Errorf("detailed item %s %s = %s, want %s", id, field, got, want)
		}
	}
	for field, want := range map[string]string{
		"CanDelete": "true", "EnableMediaSourceDisplay": "true", "LocalTrailerCount": "0", "LockData": "false", "PlayAccess": `"Full"`, "SpecialFeatureCount": "0",
	} {
		if got := string(wire[field]); got != want {
			t.Errorf("detailed item %s %s = %s, want %s", id, field, got, want)
		}
	}
	wantDownload := "false"
	if movie {
		wantDownload = "true"
	}
	if got := string(wire["CanDownload"]); got != wantDownload {
		t.Errorf("detailed item %s CanDownload = %s, want %s", id, got, wantDownload)
	}
	if movie {
		for _, field := range []string{"ChildCount", "RecursiveItemCount"} {
			if value, found := wire[field]; found {
				t.Errorf("detailed movie %s unexpectedly emitted folder-only %s=%s", id, field, value)
			}
		}
	} else {
		for field, want := range map[string]string{"ChildCount": "0", "RecursiveItemCount": "1"} {
			if got := string(wire[field]); got != want {
				t.Errorf("detailed folder %s %s = %s, want %s", id, field, got, want)
			}
		}
	}
	wantDisplayID := id.String()
	if movie {
		wantDisplayID = "dbf7709c41faaa746463d67978eb863d"
	}
	if got := string(wire["DisplayPreferencesId"]); got != `"`+wantDisplayID+`"` {
		t.Errorf("detailed item %s DisplayPreferencesId = %s, want %q", id, got, wantDisplayID)
	}
	if _, found := wire["AlbumCount"]; found {
		t.Errorf("detailed item %s unexpectedly emitted absent AlbumCount", id)
	}
}

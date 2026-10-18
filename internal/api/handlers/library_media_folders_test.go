// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	"github.com/deadfin-org/deadfin/internal/api/handlers"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/dtobuild"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/store"
)

// GET /Library/MediaFolders ports LibraryController.GetMediaFolders: the
// collection folders and the hidden playlists folder, without disabled
// libraries, ordered by SortName, filtered by isHidden, as a QueryResult of
// BaseItemDto under the RequiresElevation policy.
func TestLibraryMediaFoldersMatchesUpstreamShape(t *testing.T) {
	h := newP104Harness(t)
	admin := h.createUser(t, "Admin", "password", json.RawMessage(`{"IsAdministrator":true,"EnableAllFolders":true}`), json.RawMessage(`{}`))
	h.identities["admin"] = auth.Identity{Authenticated: true, Administrator: true, UserID: admin.ID}
	items := store.NewItemStore(h.stores)
	if err := items.BootstrapSystemFolders(t.Context(), t.TempDir()); err != nil {
		t.Fatalf("bootstrap system folders: %v", err)
	}
	movies := mediaFolder(t, items, "00000000-0000-0000-0000-000000000301", "Movies", item.CollectionTypeMovies)
	anime := mediaFolder(t, items, "00000000-0000-0000-0000-000000000302", "Anime", item.CollectionTypeTVShows)
	disabled := mediaFolder(t, items, "00000000-0000-0000-0000-000000000303", "Archive", item.CollectionTypeMovies)
	topology := &fakeLibraryTopology{folders: []library.VirtualFolder{
		{CollectionFolderID: movies, Name: "Movies", Options: library.LibraryOptions{Enabled: true}},
		{CollectionFolderID: anime, Name: "Anime", Options: library.LibraryOptions{Enabled: true}},
		{CollectionFolderID: disabled, Name: "Archive", Options: library.LibraryOptions{Enabled: false}},
	}}
	builder := dtobuild.NewBuilder(items, dtobuild.NewStoreEnricher(items, h.users, nil), h.identity.ID.String())
	handlers.RegisterMediaFolders(h.rt, topology, items, builder)

	want := router.Route{Method: http.MethodGet, Pattern: "/Library/MediaFolders", OperationID: "GetMediaFolders", Policy: string(auth.PolicyRequiresElevation)}
	if got, found := routeByOperation(h.rt.Routes(), want.OperationID); !found || got != want {
		t.Fatalf("route = %#v, found %t, want %#v", got, found, want)
	}

	for _, tc := range []struct {
		query     string
		wantNames []string
		wantTypes []string
	}{
		{query: "", wantNames: []string{"Anime", "Movies", "Playlists"}, wantTypes: []string{"CollectionFolder", "CollectionFolder", "ManualPlaylistsFolder"}},
		{query: "?isHidden=false", wantNames: []string{"Anime", "Movies"}, wantTypes: []string{"CollectionFolder", "CollectionFolder"}},
		{query: "?IsHidden=true", wantNames: []string{"Playlists"}, wantTypes: []string{"ManualPlaylistsFolder"}},
	} {
		rec := p104Call(h.rt, http.MethodGet, "/Library/MediaFolders"+tc.query, "", "", "application/json", "admin", p104MediaBrowserMetadata)
		assertStatus(t, rec, http.StatusOK)
		body := p104RawObject(t, rec.Body.Bytes())
		for _, field := range []string{"Items", "TotalRecordCount", "StartIndex"} {
			if _, ok := body[field]; !ok {
				t.Fatalf("%s: body has no %s: %s", tc.query, field, rec.Body.String())
			}
		}
		rows := p104RawArray(t, body["Items"])
		var names, types []string
		for _, raw := range rows {
			row := p104RawObject(t, raw)
			names = append(names, p104String(t, row["Name"]))
			types = append(types, p104String(t, row["Type"]))
			if _, ok := row["CollectionType"]; !ok {
				t.Errorf("%s: row %s has no CollectionType", tc.query, raw)
			}
			if _, ok := row["UserData"]; ok {
				t.Errorf("%s: row %s has UserData, want none without a user", tc.query, raw)
			}
		}
		if !reflect.DeepEqual(names, tc.wantNames) || !reflect.DeepEqual(types, tc.wantTypes) {
			t.Fatalf("%s: names, types = %q, %q, want %q, %q", tc.query, names, types, tc.wantNames, tc.wantTypes)
		}
		var total int
		if err := json.Unmarshal(body["TotalRecordCount"], &total); err != nil || total != len(tc.wantNames) {
			t.Fatalf("%s: TotalRecordCount = %s, want %d", tc.query, body["TotalRecordCount"], len(tc.wantNames))
		}
	}

	rec := p104Call(h.rt, http.MethodGet, "/Library/MediaFolders?isHidden=maybe", "", "", "application/json", "admin", p104MediaBrowserMetadata)
	assertStatus(t, rec, http.StatusBadRequest)
}

func mediaFolder(t *testing.T, items *store.ItemStore, id, name string, collection item.CollectionType) [16]byte {
	t.Helper()
	folderID := p104UUID(t, id)
	path := "/library/" + name
	value := item.Item{
		ID: folderID, Kind: item.KindCollectionFolder, IsFolder: true, Name: &name, SortName: &name, Path: &path,
		Extra: item.ExtraFields{CollectionFolder: &item.CollectionFolderExtra{CollectionType: &collection}},
	}
	if err := items.Upsert(t.Context(), value); err != nil {
		t.Fatalf("upsert %s: %v", name, err)
	}
	return folderID
}

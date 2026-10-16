// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/deadfin-org/deadfin/internal/api/handlers"
	"github.com/deadfin-org/deadfin/internal/api/middleware"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
)

// TestItemRestrictedFolderUserReadsPerson covers #610: a user with
// EnableAllFolders false opens a Person by ID, as upstream
// BaseItem.IsVisibleStandalone admits an item with no collection folder. A
// movie outside the enabled folders stays not found.
func TestItemRestrictedFolderUserReadsPerson(t *testing.T) {
	stores, err := sqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("open restricted item store: %v", err)
	}
	t.Cleanup(func() {
		if err := stores.Close(); err != nil {
			t.Errorf("close restricted item store: %v", err)
		}
	})
	allowedRootID, deniedRootID := itemsHandlerUUID(t, 610), itemsHandlerUUID(t, 611)
	user := store.User{ID: itemsHandlerUUID(t, 612), Policy: []byte(`{"EnableAllFolders":false,"EnabledFolders":["` + allowedRootID.String() + `"]}`)}
	allowedRoot := item.Item{ID: allowedRootID, Kind: item.KindFolder, IsFolder: true, Name: itemsHandlerString("allowed root")}
	deniedRoot := item.Item{ID: deniedRootID, Kind: item.KindFolder, IsFolder: true, Name: itemsHandlerString("denied root")}
	deniedMovie := item.Item{ID: itemsHandlerUUID(t, 613), Kind: item.KindMovie, ParentID: &deniedRootID, Name: itemsHandlerString("denied movie")}
	person := item.Item{ID: itemsHandlerUUID(t, 614), Kind: item.KindPerson, Name: itemsHandlerString("Tracey Bonner")}
	items := store.NewItemStore(stores)
	for _, value := range []item.Item{allowedRoot, deniedRoot, deniedMovie, person} {
		if err := items.Upsert(t.Context(), value); err != nil {
			t.Fatalf("seed %s: %v", value.ID, err)
		}
	}

	users := itemsAccessUsers{values: map[dto.UUID]store.User{user.ID: user}}
	authorizer := middleware.NewAuthorizer(itemsAccessResolver{identities: map[string]auth.Identity{user.ID.String(): {Authenticated: true, UserID: user.ID}}}, itemsAccessPolicy{}, users)
	rt := router.New(authorizer.Wrap)
	handlers.RegisterItems(rt, &itemsAccessReader{items: items}, &itemsAccessBuilder{}, clock.NewReal(), handlers.ItemServices{Users: users})

	for _, tc := range []struct {
		name string
		path string
		want int
	}{
		{name: "person by ID", path: "/Items/" + person.ID.Dashed() + "?userId=" + user.ID.Dashed(), want: http.StatusOK},
		{name: "person by legacy user route", path: "/Users/" + user.ID.Dashed() + "/Items/" + person.ID.Dashed(), want: http.StatusOK},
		{name: "movie outside enabled folders", path: "/Items/" + deniedMovie.ID.Dashed() + "?userId=" + user.ID.Dashed(), want: http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			record := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, tc.path, nil)
			request.Header.Set("X-Item-Access-Actor", user.ID.String())
			rt.ServeHTTP(record, request)
			if record.Code != tc.want {
				t.Fatalf("GET %s = %d, want %d; body=%s", tc.path, record.Code, tc.want, record.Body.String())
			}
		})
	}
}

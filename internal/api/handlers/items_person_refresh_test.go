// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/deadfin-org/deadfin/internal/api/handlers"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
)

type recordingPersonRefresher struct {
	seen    []item.Item
	refresh bool
}

func (refresher *recordingPersonRefresher) RefreshOnDemand(_ context.Context, value item.Item) (item.Item, bool, error) {
	refresher.seen = append(refresher.seen, value)
	return value, refresher.refresh, nil
}

// TestItemDetailRefreshesPersonOnDemand pins the
// RefreshItemOnDemandIfNeeded call of GetItem: a Person goes to the refresher
// before the response is built, and a refreshed person is read again.
func TestItemDetailRefreshesPersonOnDemand(t *testing.T) {
	userID, personID, movieID := itemsHandlerUUID(t, 60), itemsHandlerUUID(t, 61), itemsHandlerUUID(t, 62)
	for _, tc := range []struct {
		name    string
		kind    item.Kind
		refresh bool
		seen    int
		queries int
	}{
		{name: "person refreshed", kind: item.KindPerson, refresh: true, seen: 1, queries: 2},
		{name: "person not refreshed", kind: item.KindPerson, seen: 1, queries: 1},
		{name: "movie", kind: item.KindMovie, refresh: true, queries: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := personID
			if tc.kind == item.KindMovie {
				id = movieID
			}
			reader := &itemsHandlerReader{result: store.ItemQueryResult{Items: []item.Item{{ID: id, Kind: tc.kind}}, TotalRecordCount: 1}}
			builder := &itemsHandlerBuilder{items: []dto.BaseItemDto{{ID: id}}}
			refresher := &recordingPersonRefresher{refresh: tc.refresh}
			rt := router.New()
			handlers.RegisterItems(rt, reader, builder, clock.NewReal(), handlers.PersonRefresher(refresher))

			record := httptest.NewRecorder()
			rt.ServeHTTP(record, httptest.NewRequest(http.MethodGet, "/Items/"+id.Dashed()+"?userId="+userID.Dashed(), nil))
			if record.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body=%s", record.Code, record.Body.String())
			}
			if len(refresher.seen) != tc.seen || reader.queries != tc.queries || builder.detailedCalls != 1 {
				t.Fatalf("refresher/reader/builder calls = %d/%d/%d, want %d/%d/1", len(refresher.seen), reader.queries, builder.detailedCalls, tc.seen, tc.queries)
			}
			if tc.queries == 2 {
				if got := reader.seen[1]; len(got.ItemIDs) != 1 || got.ItemIDs[0] != id || got.UserID == nil || *got.UserID != userID {
					t.Fatalf("re-read query = %#v, want the person and the request user", got)
				}
			}
		})
	}
}

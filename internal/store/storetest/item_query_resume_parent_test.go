// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
	storetestpkg "github.com/deadfin-org/deadfin/internal/store/storetest"
)

// GetResumeItems passes ParentId with Recursive = true
// (upstream/jellyfin/Jellyfin.Api/Controllers/ItemsController.cs:978-997), so
// a resume query keeps only the in-progress items under the parent. The
// bounded Resume relation must not drop that scope.

func TestItemQueryResumeParentScopeSQLite(t *testing.T) {
	stores, err := sqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("open SQLite: %v", err)
	}
	t.Cleanup(func() { _ = stores.Close() })
	assertResumeParentScope(t, stores)
}

func TestItemQueryResumeParentScopePostgres(t *testing.T) {
	assertResumeParentScope(t, storetestpkg.OpenPostgres(t))
}

func assertResumeParentScope(t *testing.T, stores store.Stores) {
	t.Helper()
	f := newRecursiveParentFixture(t, stores)
	items := store.NewItemStore(stores)
	id := func(n int) dto.UUID { return itemQueryPlanUUID(t, 90_000+n) }
	episode2, movieA, movieB, movieC := id(14), id(15), id(20), id(21)
	// Newest first: Movie C, Movie B, Movie A, Episode 2.
	for offset, itemID := range []dto.UUID{episode2, movieA, movieB, movieC} {
		played := time.Date(2026, 10, 1, offset, 0, 0, 0, time.UTC)
		if err := items.UpsertUserData(t.Context(), store.UserData{UserID: f.user.ID, CustomKey: itemID.String(), ItemID: itemID, PositionTicks: 1000, LastPlayed: &played}); err != nil {
			t.Fatalf("upsert user data %s: %v", itemID, err)
		}
	}
	resume := func(mutate func(*store.ItemQuery)) store.ItemQuery {
		resumable, collapse := true, false
		query := store.ItemQuery{
			UserID: &f.user.ID, User: &f.user, IsResumable: &resumable, Recursive: true, IncludeOwnedItems: true,
			CollapseBoxSetItems: &collapse, EnableTotalRecordCount: true,
			SortBy: []store.ItemSortBy{store.ItemSortByDatePlayed}, SortOrder: []store.SortOrder{store.SortOrderDescending},
		}
		if mutate != nil {
			mutate(&query)
		}
		return query
	}
	parent := func(parentID dto.UUID) func(*store.ItemQuery) {
		return func(query *store.ItemQuery) { query.ParentID = &parentID }
	}
	one := 1
	cases := []struct {
		name  string
		query store.ItemQuery
		want  []dto.UUID
		total int
	}{
		{name: "no parent", query: resume(nil), want: []dto.UUID{movieC, movieB, movieA, episode2}},
		{name: "series", query: resume(parent(f.series)), want: []dto.UUID{episode2}},
		{name: "top library", query: resume(parent(f.topLibrary)), want: []dto.UUID{movieA, episode2}},
		{name: "nested library", query: resume(parent(f.nestedLibrary)), want: []dto.UUID{movieB}},
		{name: "box set", query: resume(parent(f.boxSet)), want: []dto.UUID{movieB, movieA}},
		{name: "playlist", query: resume(parent(f.playlist)), want: []dto.UUID{movieC, episode2}},
		{name: "missing parent", query: resume(parent(f.missing))},
		{name: "top library page", query: resume(func(query *store.ItemQuery) {
			parent(f.topLibrary)(query)
			query.Limit = &one
		}), want: []dto.UUID{movieA}, total: 2},
		{name: "top library movies", query: resume(func(query *store.ItemQuery) {
			parent(f.topLibrary)(query)
			query.IncludeItemTypes = []item.Kind{item.KindMovie}
		}), want: []dto.UUID{movieA}},
		{name: "top library without movies", query: resume(func(query *store.ItemQuery) {
			parent(f.topLibrary)(query)
			query.ExcludeItemTypes = []item.Kind{item.KindMovie}
		}), want: []dto.UUID{episode2}},
		{name: "audio only", query: resume(func(query *store.ItemQuery) {
			query.MediaTypes = []item.MediaType{item.MediaTypeAudio}
		})},
		{name: "search term", query: resume(func(query *store.ItemQuery) {
			query.SearchTerm = "movie b"
		}), want: []dto.UUID{movieB}},
		{name: "excluded ids", query: resume(func(query *store.ItemQuery) {
			query.ExcludeItemIDs = []dto.UUID{movieC, movieB}
		}), want: []dto.UUID{movieA, episode2}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := items.Query(t.Context(), tc.query)
			if err != nil {
				t.Fatalf("query: %v", err)
			}
			got := make([]dto.UUID, 0, len(result.Items))
			for _, value := range result.Items {
				got = append(got, value.ID)
			}
			want := tc.want
			if want == nil {
				want = []dto.UUID{}
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("ids = %v, want %v", got, want)
			}
			total := tc.total
			if total == 0 {
				total = len(want)
			}
			if result.TotalRecordCount != total {
				t.Fatalf("total = %d, want %d", result.TotalRecordCount, total)
			}
		})
	}
}

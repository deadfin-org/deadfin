// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/store"
)

func TestItemStorePeopleRefreshSQLite(t *testing.T) {
	runItemStorePeopleRefresh(t, openReviewerItemStore)
}

func TestItemStorePeopleRefreshPostgres(t *testing.T) {
	runItemStorePeopleRefresh(t, openPostgresReviewerItemStore)
}

func runItemStorePeopleRefresh(t *testing.T, open func(*testing.T) (*store.ItemStore, store.Stores)) {
	t.Helper()
	refreshed := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

	t.Run("PeopleMissingMetadata lists never-refreshed people without an overview or a Primary image", func(t *testing.T) {
		items, _ := open(t)
		overview := "An actor."
		bare := library.PersonEntity("Bare")
		noImage := library.PersonEntity("No Image")
		noImage.Overview = &overview
		noOverview := library.PersonEntity("No Overview")
		noOverview.ImageInfos = []item.ImageInfo{{Type: item.ImageTypePrimary, Path: "https://image.tmdb.org/t/p/original/a.jpg"}}
		complete := library.PersonEntity("Complete")
		complete.Overview = &overview
		complete.ImageInfos = noOverview.ImageInfos
		done := library.PersonEntity("Done")
		done.DateLastRefreshed = &refreshed
		genre := library.PersonEntity("Not A Person")
		genre.Kind = item.KindGenre
		genre.ID = dto.UUID{0xfe}
		for _, value := range []item.Item{bare, noImage, noOverview, complete, done, genre} {
			if err := items.Upsert(t.Context(), value); err != nil {
				t.Fatalf("upsert %s: %v", *value.Name, err)
			}
		}
		got, err := items.PeopleMissingMetadata(t.Context())
		if err != nil {
			t.Fatalf("PeopleMissingMetadata: %v", err)
		}
		want := []dto.UUID{bare.ID, noImage.ID, noOverview.ID}
		slices.SortFunc(want, func(a, b dto.UUID) int { return slices.Compare(a[:], b[:]) })
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("PeopleMissingMetadata = %v, want %v", got, want)
		}
	})

	t.Run("a scan keeps refreshed person metadata and adds credit provider ids", func(t *testing.T) {
		items, _ := open(t)
		parent, movie, person := scannedPlanPeopleFixture(t)
		if err := items.Upsert(t.Context(), parent); err != nil {
			t.Fatalf("upsert parent: %v", err)
		}
		if _, err := items.ReconcileScannedPlan(t.Context(), parent.ID, []item.Item{movie}, nil, []item.Item{person}); err != nil {
			t.Fatalf("first reconcile: %v", err)
		}
		stored, err := items.GetBatch(t.Context(), []dto.UUID{person.ID})
		if err != nil || len(stored) != 1 {
			t.Fatalf("GetBatch = %v, %v", stored, err)
		}
		edited := stored[0]
		overview := "A refreshed biography."
		edited.Overview = &overview
		edited.DateLastRefreshed = &refreshed
		edited.Values.ProductionLocations = []string{"Somewhere"}
		edited.ProviderIDs.Set("Imdb", "nm1")
		edited.ProviderIDs.Set("Tmdb", "1")
		if err := items.EditItems(t.Context(), []item.Item{edited}, nil); err != nil {
			t.Fatalf("EditItems: %v", err)
		}

		candidate := person
		candidate.ProviderIDs.Set("Tmdb", "2")
		if _, err := items.ReconcileScannedPlan(t.Context(), parent.ID, []item.Item{movie}, nil, []item.Item{candidate}); err != nil {
			t.Fatalf("second reconcile: %v", err)
		}
		after, err := items.GetBatch(t.Context(), []dto.UUID{person.ID})
		if err != nil || len(after) != 1 {
			t.Fatalf("GetBatch = %v, %v", after, err)
		}
		got := after[0]
		if got.Overview == nil || *got.Overview != overview || got.DateLastRefreshed == nil || !got.DateLastRefreshed.Equal(refreshed) {
			t.Errorf("person Overview = %v, DateLastRefreshed = %v; want the refreshed values", got.Overview, got.DateLastRefreshed)
		}
		if !reflect.DeepEqual(got.Values.ProductionLocations, []string{"Somewhere"}) {
			t.Errorf("ProductionLocations = %v, want the refreshed value", got.Values.ProductionLocations)
		}
		if tmdb, _ := got.ProviderIDs.Get("Tmdb"); tmdb != "2" {
			t.Errorf("Tmdb id = %q, want the credit id 2 (SetProviderId on a different value)", tmdb)
		}
		if imdb, _ := got.ProviderIDs.Get("Imdb"); imdb != "nm1" {
			t.Errorf("Imdb id = %q, want the stored id", imdb)
		}
	})
}

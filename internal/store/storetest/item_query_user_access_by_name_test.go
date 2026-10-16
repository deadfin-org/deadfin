// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
)

// TestItemStoreQueryRestrictedUserByNameItemsSQLite verifies that a
// restricted-folder user reads an item-by-name with no library ancestry by
// explicit ID, as upstream BaseItem.IsVisibleStandalone admits an item with no
// collection folder. Library items outside the enabled folders stay hidden.
func TestItemStoreQueryRestrictedUserByNameItemsSQLite(t *testing.T) {
	runItemStoreQueryRestrictedUserByNameItems(t, openItemStore)
}

func TestItemStoreQueryRestrictedUserByNameItemsPostgres(t *testing.T) {
	runItemStoreQueryRestrictedUserByNameItems(t, openPostgresItemStore)
}

func runItemStoreQueryRestrictedUserByNameItems(t *testing.T, open func(*testing.T) *store.ItemStore) {
	t.Helper()
	items := open(t)
	rootA := fullUserAccessItem(t, 2000, item.KindFolder, "library a", nil)
	rootA.IsFolder = true
	rootB := fullUserAccessItem(t, 2001, item.KindFolder, "library b", nil)
	rootB.IsFolder = true
	allowedMovie := fullUserAccessItem(t, 2010, item.KindMovie, "allowed movie", &rootA.ID)
	deniedMovie := fullUserAccessItem(t, 2011, item.KindMovie, "denied movie", &rootB.ID)
	deniedArtist := fullUserAccessItem(t, 2012, item.KindMusicArtist, "denied folder artist", &rootB.ID)
	deniedArtist.IsFolder = true
	person := fullUserAccessItem(t, 2020, item.KindPerson, "person", nil)
	genre := fullUserAccessItem(t, 2021, item.KindGenre, "genre", nil)
	studio := fullUserAccessItem(t, 2022, item.KindStudio, "studio", nil)
	ratedPerson := fullUserAccessItem(t, 2023, item.KindPerson, "rated person", nil)
	ratedPerson.InheritedParentalRatingValue = fullUserAccessInt(11)
	taggedPerson := fullUserAccessItem(t, 2024, item.KindPerson, "tagged person", nil)
	taggedPerson.Values.Tags = []string{"blocked"}
	for _, value := range []item.Item{rootA, rootB, allowedMovie, deniedMovie, deniedArtist, person, genre, studio, ratedPerson, taggedPerson} {
		if err := items.Upsert(t.Context(), value); err != nil {
			t.Fatalf("upserting by-name access fixture item %s: %v", value.ID, err)
		}
	}
	fixture := fullUserAccessFixture{items: items, userID: fullUserAccessUUID(t, 2099)}
	restricted := fixture.user(`{"EnableAllFolders":false,"EnabledFolders":["` + rootA.ID.String() + `"]}`)

	t.Run("explicit ID reads a person genre and studio and still hides library items outside the enabled folders", func(t *testing.T) {
		for _, value := range []item.Item{person, genre, studio, allowedMovie} {
			fixture.assertQuery(t, store.ItemQuery{User: restricted, ItemIDs: []dto.UUID{value.ID}, EnableTotalRecordCount: true}, []dto.UUID{value.ID}, 1)
		}
		for _, value := range []item.Item{deniedMovie, deniedArtist} {
			fixture.assertQuery(t, store.ItemQuery{User: restricted, ItemIDs: []dto.UUID{value.ID}, EnableTotalRecordCount: true}, []dto.UUID{}, 0)
		}
	})

	t.Run("parental rating and blocked tags still apply to items by name", func(t *testing.T) {
		user := fixture.user(`{"EnableAllFolders":false,"EnabledFolders":["` + rootA.ID.String() + `"],"MaxParentalRating":10,"BlockedTags":["blocked"]}`)
		fixture.assertQuery(t, store.ItemQuery{
			User:                   user,
			ItemIDs:                []dto.UUID{person.ID, ratedPerson.ID, taggedPerson.ID},
			EnableTotalRecordCount: true,
		}, []dto.UUID{person.ID}, 1)
	})

	t.Run("a list without explicit IDs keeps the folder scope", func(t *testing.T) {
		fixture.assertQuery(t, store.ItemQuery{
			User:                   restricted,
			Recursive:              true,
			IncludeItemTypes:       []item.Kind{item.KindPerson, item.KindGenre, item.KindStudio, item.KindMovie},
			EnableTotalRecordCount: true,
		}, []dto.UUID{allowedMovie.ID}, 1)
	})
}

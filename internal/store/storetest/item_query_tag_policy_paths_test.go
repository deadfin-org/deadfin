// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
)

func TestItemStoreQueryTagPolicyPathsSQLite(t *testing.T) {
	runItemStoreQueryTagPolicyPaths(t, openItemStore)
}

func TestItemStoreQueryTagPolicyPathsPostgres(t *testing.T) {
	runItemStoreQueryTagPolicyPaths(t, openPostgresItemStore)
}

// runItemStoreQueryTagPolicyPaths checks that BlockedTags and AllowedTags
// match a tag on the item, on its series and on its library, and that an
// item with no tagged relative is not lost to a NULL comparison (#761).
func runItemStoreQueryTagPolicyPaths(t *testing.T, open func(*testing.T) *store.ItemStore) {
	t.Helper()
	items := open(t)
	tagged := groupingAccessItem(t, 1001, item.KindFolder, "tagged library")
	tagged.IsFolder = true
	tagged.Values.Tags = []string{"marked"}
	plainLibrary := groupingAccessItem(t, 1002, item.KindFolder, "plain library")
	plainLibrary.IsFolder = true
	series := groupingAccessItem(t, 1003, item.KindSeries, "series")
	series.IsFolder, series.ParentID = true, &plainLibrary.ID
	series.Values.Tags = []string{"marked"}
	season := groupingAccessItem(t, 1004, item.KindSeason, "season")
	season.IsFolder, season.ParentID = true, &series.ID
	bySeries := groupingAccessItem(t, 1005, item.KindEpisode, "b by series")
	bySeries.ParentID, bySeries.SeriesID = &season.ID, &series.ID
	byLibrary := groupingAccessItem(t, 1006, item.KindMovie, "c by library")
	byLibrary.ParentID = &tagged.ID
	byItem := groupingAccessItem(t, 1007, item.KindMovie, "a by item")
	byItem.ParentID = &plainLibrary.ID
	byItem.Values.Tags = []string{"marked"}
	plain := groupingAccessItem(t, 1008, item.KindMovie, "d plain")
	plain.ParentID = &plainLibrary.ID
	for _, value := range []item.Item{tagged, plainLibrary, series, season, bySeries, byLibrary, byItem, plain} {
		groupingAccessUpsert(t, items, value)
	}
	query := func(policy string) store.ItemQuery {
		return store.ItemQuery{
			User:             &store.User{ID: groupingAccessUUID(t, 1010), Policy: []byte(policy)},
			Recursive:        true,
			IncludeItemTypes: []item.Kind{item.KindEpisode, item.KindMovie},
		}
	}
	groupingAccessAssertQuery(t, items, query(`{"EnableAllFolders":true,"BlockedTags":["Marked"]}`), []dto.UUID{plain.ID}, 1)
	groupingAccessAssertQuery(t, items, query(`{"EnableAllFolders":true,"AllowedTags":["marked"]}`),
		[]dto.UUID{byItem.ID, bySeries.ID, byLibrary.ID}, 3)
	groupingAccessAssertQuery(t, items, query(`{"EnableAllFolders":true,"BlockedTags":["other"]}`),
		[]dto.UUID{byItem.ID, bySeries.ID, byLibrary.ID, plain.ID}, 4)
}

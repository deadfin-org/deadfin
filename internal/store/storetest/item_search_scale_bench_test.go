// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
)

// BenchmarkSearchScoresAtLibraryScale measures the GET /Search/Hints
// candidate search on the library of BenchmarkReconcileAtLibraryScale
// (about 47,000 items, 31,000 of them people) and a Live TV guide of
// production size: 11,000 channels and 400,000 programmes (deadfin#885).
// Search hints filter no kind, so the search visits every item, and most of
// them are programmes. The rows carry the path, overview, and extra facts
// of a live library, and people, episodes, and tracks carry provider ids, as
// they do after a metadata refresh. The search must read the provider ids of
// the anime titles that match, not of every item (deadfin#826). It runs on
// file-backed SQLite and, when DEADFIN_TEST_POSTGRES_DSN is set, on
// PostgreSQL.
//
//	go test -run '^$' -bench SearchScoresAtLibraryScale ./internal/store/storetest/
func BenchmarkSearchScoresAtLibraryScale(b *testing.B) {
	forEachScaleBackend(b, benchmarkSearchScoresAtLibraryScale)
}

func benchmarkSearchScoresAtLibraryScale(b *testing.B, stores store.Stores) {
	ctx := b.Context()
	items := store.NewItemStore(stores)
	writeScaleFixture(b, ctx, stores)
	writeLiveTVScaleFixture(b, ctx, stores)
	if err := items.RepairDerived(ctx); err != nil {
		b.Fatalf("initial global repair: %v", err)
	}
	q := scaleSQL{stores.Dialect()}
	text := "hex(id)"
	if q.postgres() {
		text = "CAST(id AS TEXT)"
	}
	overview := "'" + strings.Repeat("A long overview of the plot that runs on. ", 15) + "'"
	extra := `'{"tags":["a","b"],"studios":["Studio One","Studio Two"],"remoteTrailers":[{"url":"https://www.youtube.com/watch?v=0123456789a"}],"productionLocations":["United States of America"]}'`
	for _, statement := range []string{
		// The fixture names are ASCII, so LOWER is item.CleanName here.
		"UPDATE items SET clean_name = LOWER(name)",
		"UPDATE items SET path = '/media/library/' || name || '/' || name || ' (2001) [Bluray-1080p].mkv', overview = " + overview + ", extra = " + extra + " WHERE kind NOT IN ('Person', 'LiveTvChannel', 'LiveTvProgram') OR id IN (SELECT person_id FROM people_map)",
		"UPDATE items SET overview = " + overview + " WHERE kind = 'LiveTvProgram'",
		"INSERT INTO provider_ids (item_id, provider, value) SELECT id, 'Tmdb', " + text + " FROM items WHERE kind IN ('Person', 'Episode', 'Audio')",
		"INSERT INTO provider_ids (item_id, provider, value) SELECT id, 'Imdb', " + text + " FROM items WHERE kind IN ('Person', 'Episode')",
		"INSERT INTO provider_ids (item_id, provider, value) SELECT id, 'Tvdb', " + text + " FROM items WHERE kind = 'Episode'",
		// Every series is an AniList anime with two titles.
		"INSERT INTO provider_ids (item_id, provider, value) SELECT id, 'AniList', " + text + " FROM items WHERE kind = 'Series'",
		"INSERT INTO anime_titles (provider, external_id, ordinal, title, clean_title, fold_title) SELECT provider, value, 0, 'Anime ' || value, 'anime ' || LOWER(value), 'anime' || LOWER(value) FROM provider_ids WHERE provider = 'AniList'",
		"INSERT INTO anime_titles (provider, external_id, ordinal, title, clean_title, fold_title) SELECT provider, value, 1, 'Other ' || value, 'other ' || LOWER(value), 'other' || LOWER(value) FROM provider_ids WHERE provider = 'AniList'",
	} {
		if _, err := stores.Writer().ExecContext(ctx, statement); err != nil {
			b.Fatalf("prepare the search fixture: %v", err)
		}
	}
	analyzeScaleFixture(b, ctx, stores)
	admin := store.User{ID: scaleUUID(990_001), Policy: []byte(`{"IsAdministrator":true}`)}
	viewer := store.User{ID: scaleUUID(990_002), Policy: []byte(`{"EnableAllFolders":true}`)}
	// listed may see only the channels of the channel lists shared with it
	// (D-105); none are.
	listed := store.User{ID: scaleUUID(990_003), Policy: []byte(`{"EnableAllFolders":true,"LimitLiveTvToSharedLists":true}`)}
	// The candidate query of the search hints handler.
	notVirtual, noCollapse := false, false
	candidates := func(term string, user *store.User) store.ItemQuery {
		return store.ItemQuery{SearchTerm: term, IsVirtualItem: &notVirtual, IncludeOwnedItems: true, CollapseBoxSetItems: &noCollapse,
			ExcludeItemTypes: []item.Kind{item.KindYear, item.KindFolder, item.KindCollectionFolder}, User: user, UserID: &user.ID}
	}
	// only and except are the candidate queries of a search hints request
	// with includeItemTypes and excludeItemTypes.
	only := func(term string, kinds ...item.Kind) store.ItemQuery {
		query := candidates(term, &admin)
		query.ExcludeItemTypes, query.IncludeItemTypes = nil, kinds
		return query
	}
	except := func(term string, kinds ...item.Kind) store.ItemQuery {
		query := candidates(term, &admin)
		query.ExcludeItemTypes = append(query.ExcludeItemTypes, kinds...)
		return query
	}
	for _, scenario := range []struct {
		name    string
		query   store.ItemQuery
		matches int
	}{
		{"one match", candidates("Movie 512", &admin), 1},
		{"one match, not an administrator", candidates("Movie 512", &viewer), 1},
		{"many matches", candidates("Movie", &admin), 100},
		{"anime title", candidates("Anime", &admin), 100},
		{"no match", candidates("The Irishman", &admin), 0},
		{"no match, not an administrator", candidates("The Irishman", &viewer), 0},
		{"programme title", candidates("Guide Show 61234", &admin), 5},
		{"programme title, not an administrator", candidates("Guide Show 61234", &viewer), 5},
		{"programme title, shared lists only", candidates("Guide Show 61234", &listed), 0},
		{"short term", candidates("show", &admin), 100},
		{"programmes only", only("Guide Show 61234", item.KindLiveTvProgram), 5},
		{"movies only", only("Movie 512", item.KindMovie), 1},
		{"Live TV excluded", except("Movie 512", item.KindTvChannel, item.KindLiveTvProgram), 1},
	} {
		b.Run(scenario.name, func(b *testing.B) {
			for range b.N {
				scores, err := items.SearchScores(ctx, scenario.query, 100)
				if err != nil || len(scores) != scenario.matches {
					b.Fatalf("search scores: %d, %v; want %d", len(scores), err, scenario.matches)
				}
			}
		})
	}
}

// writeLiveTVScaleFixture adds a Live TV guide of production size: 11,000
// channels and 400,000 programmes over three days, as an IPTV lineup with an
// XMLTV guide stores them. The programmes repeat 70,000 titles, so a title
// such as "Guide Show 61234" names five programmes. No title contains a word
// of the library fixture.
func writeLiveTVScaleFixture(b *testing.B, ctx context.Context, stores store.Stores) {
	b.Helper()
	const (
		channels   = 11000
		programmes = 400000
		titles     = 70000
	)
	q := scaleSQL{stores.Dialect()}
	tx, err := stores.Writer().BeginTx(ctx, nil)
	if err != nil {
		b.Fatalf("begin Live TV fixture: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	insert := &scaleBatch{b: b, ctx: ctx, tx: tx, q: q,
		insert: "INSERT INTO items (id, kind, name, channel_id, start_date, end_date, playlist_owner_user_id, source_type, is_folder, is_virtual_item, is_in_mixed_folder, is_locked, is_movie, is_series, is_repeat, is_placeholder, open_access)",
		row:    "(?, ?, ?, ?, ?, ?, ?, '2', FALSE, FALSE, FALSE, FALSE, FALSE, FALSE, FALSE, FALSE, FALSE)"}
	zero := q.id(scaleUUID(0))
	const first = 2_000_000
	start := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	for index := range channels {
		insert.add(q.id(scaleUUID(first+index)), item.KindLiveTvChannel.String(), fmt.Sprintf("Channel %d", index), nil, nil, nil, zero)
	}
	for index := range programmes {
		channel := index % channels
		slot := start.Add(time.Duration(index/channels) * 2 * time.Hour)
		insert.add(q.id(scaleUUID(first+channels+index)), item.KindLiveTvProgram.String(), fmt.Sprintf("Guide Show %d", index%titles),
			q.id(scaleUUID(first+channel)), stores.Dialect().TimeValue(slot), stores.Dialect().TimeValue(slot.Add(2*time.Hour)), zero)
	}
	insert.flush()
	if err := tx.Commit(); err != nil {
		b.Fatalf("commit Live TV fixture: %v", err)
	}
}

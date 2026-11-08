// SPDX-License-Identifier: GPL-2.0-only

package store_test

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/postgres"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
)

// TestTranslateItemQueryScalarFiltersAreBoundAndApplied defends the direct
// BaseItemRepository predicates that have no satellite table. Every request
// value remains a dialect-bound argument in the page and pre-page count SQL.
// Sources: BaseItemRepository.TranslateQuery.cs:44-115,239-339,389-417,
// 742-767, 938-950, 1009-1028, 1144-1150, and 1211-1227 at b04614e.
func TestTranslateItemQueryScalarFiltersAreBoundAndApplied(t *testing.T) {
	trueValue, falseValue := true, false
	one, two, three, four := 1, 2, 3, 4
	reference := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	channel := scalarOrderUUID(t, 91)
	owner := scalarOrderUUID(t, 92)
	parentKind := item.KindSeries

	cases := []struct {
		name  string
		query store.ItemQuery
		want  string
	}{
		{"name prefix escapes a literal wildcard", store.ItemQuery{NameStartsWith: "A_%"}, "sort_name"},
		{"name lower cursor is inclusive", store.ItemQuery{NameStartsWithOrGreater: "B"}, "LOWER(sort_name) >="},
		{"name upper cursor is exclusive", store.ItemQuery{NameLessThan: "C"}, "LOWER(sort_name) <"},
		{"clean name equality normalizes caller input", store.ItemQuery{Name: "Café"}, "clean_name ="},
		{"raw name equality is case folded but not cleaned", store.ItemQuery{Name: "Café", UseRawName: &trueValue}, "LOWER(name) ="},
		{"name contains searches clean and original title", store.ItemQuery{NameContains: "needle"}, "original_title"},
		{"path equality is bound", store.ItemQuery{Path: "/media/O'Reilly"}, "path ="},
		{"presentation identity equality is bound", store.ItemQuery{PresentationUniqueKey: "presentation-key"}, "presentation_unique_key ="},
		{"external item identity equality is bound", store.ItemQuery{ExternalID: "external-id"}, "external_id ="},
		{"external series identity equality is bound", store.ItemQuery{ExternalSeriesID: "external-series-id"}, "external_series_id ="},
		{"series presentation identity equality is bound", store.ItemQuery{SeriesPresentationUniqueKey: "series-key"}, "series_presentation_unique_key ="},
		{"parent type uses a correlated parent predicate", store.ItemQuery{ParentType: &parentKind}, "FROM items AS parent"},
		{"start window keeps inclusive endpoints", store.ItemQuery{MinStartDate: &reference, MaxStartDate: &reference}, "start_date <="},
		{"end window keeps inclusive endpoints", store.ItemQuery{MinEndDate: &reference, MaxEndDate: &reference}, "end_date <="},
		{"creation lower bound is inclusive", store.ItemQuery{MinDateCreated: &reference}, "date_created >="},
		{"saved lower bound excludes null values", store.ItemQuery{MinDateLastSaved: &reference}, "date_last_saved >="},
		{"locked true binds a boolean fact", store.ItemQuery{IsLocked: &trueValue}, "is_locked ="},
		{"locked false binds a boolean fact", store.ItemQuery{IsLocked: &falseValue}, "is_locked ="},
		{"movie state is a direct boolean", store.ItemQuery{IsMovie: &falseValue, IncludeItemTypes: []item.Kind{item.KindMovie}}, "is_movie ="},
		{"series state is a direct boolean", store.ItemQuery{IsSeries: &trueValue}, "is_series ="},
		{"placeholder true is a direct boolean", store.ItemQuery{IsPlaceHolder: &trueValue}, "is_placeholder ="},
		{"special season means index zero", store.ItemQuery{IsSpecialSeason: &trueValue}, "index_number = 0"},
		{"ordinary season excludes index zero", store.ItemQuery{IsSpecialSeason: &falseValue}, "index_number <> 0"},
		{"overview present rejects null and empty", store.ItemQuery{HasOverview: &trueValue}, "overview IS NOT NULL AND overview <> ''"},
		{"overview absent accepts null or empty", store.ItemQuery{HasOverview: &falseValue}, "overview IS NULL OR overview = ''"},
		{"owner present tests nullability", store.ItemQuery{HasOwnerID: &trueValue}, "owner_id IS NOT NULL"},
		{"owner absent tests nullability", store.ItemQuery{HasOwnerID: &falseValue}, "owner_id IS NULL"},
		{"minimum index is inclusive", store.ItemQuery{MinIndexNumber: &two}, "index_number >="},
		{"exact index is direct equality", store.ItemQuery{IndexNumber: &two}, "index_number ="},
		{"exact parent index is direct equality", store.ItemQuery{ParentIndexNumber: &three}, "parent_index_number ="},
		{"parent index not equal retains nulls", store.ItemQuery{ParentIndexNumberNotEquals: &three}, "parent_index_number IS NULL"},
		{"compound index cursor admits same parent at boundary", store.ItemQuery{MinParentAndIndexNumber: &store.ItemQueryParentAndIndexNumber{ParentIndexNumber: two, IndexNumber: three}}, "index_number >="},
		{"season zero uses only parent index", store.ItemQuery{AiredDuringSeason: &one}, "airs_after_season_number"},
		{"minimum width is inclusive", store.ItemQuery{MinWidth: &one}, "width >="},
		{"maximum width is inclusive", store.ItemQuery{MaxWidth: &two}, "width <="},
		{"minimum height is inclusive", store.ItemQuery{MinHeight: &three}, "height >="},
		{"maximum height is inclusive", store.ItemQuery{MaxHeight: &four}, "height <="},
		{"channel scope is parameterized", store.ItemQuery{ChannelIDs: []dto.UUID{channel}}, "channel_id IN"},
		{"owner scope is parameterized", store.ItemQuery{OwnerIDs: []dto.UUID{owner}}, "owner_id IN"},
		{"extra type scope is parameterized", store.ItemQuery{ExtraTypes: []item.ExtraType{item.ExtraTypeTrailer}}, "extra_type IN"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, dialect := range scalarOrderDialects() {
				t.Run(dialect.name, func(t *testing.T) {
					translated, err := store.Translate(tc.query, dialect.dialect)
					if err != nil {
						t.Fatalf("Translate(%s): %v", tc.name, err)
					}
					for _, sql := range []string{translated.SQL, translated.CountSQL} {
						if !strings.Contains(sql, tc.want) {
							t.Fatalf("translated SQL = %s\nwant direct scalar predicate containing %q", sql, tc.want)
						}
						if strings.Contains(sql, "O'Reilly") || strings.Contains(sql, "presentation-key") {
							t.Fatalf("translated SQL interpolated caller data: %s", sql)
						}
					}
					if !reflect.DeepEqual(translated.Args[:len(translated.CountArgs)], translated.CountArgs) {
						t.Fatalf("page filter args = %#v, want count arg prefix %#v", translated.Args[:len(translated.CountArgs)], translated.CountArgs)
					}
					if len(translated.CountArgs) == 0 {
						t.Fatalf("direct scalar query %s produced no bound values", tc.name)
					}
				})
			}
		})
	}
}

// sqliteAiringArgs are the SQLite arguments of an airing filter at
// reference: now and the start bound of each span class, then now for the
// unbounded class and for the end (#892).
func sqliteAiringArgs(reference time.Time) []any {
	now := sqlite.Dialect{}.TimeValue(reference)
	args := []any{}
	for _, limit := range []time.Duration{time.Hour, 2 * time.Hour, 4 * time.Hour, 8 * time.Hour, 24 * time.Hour, 48 * time.Hour, 7 * 24 * time.Hour} {
		args = append(args, now, sqlite.Dialect{}.TimeValue(reference.Add(-limit)))
	}
	return append(args, now, now)
}

// TestTranslateItemQueryScalarClockBoundaries rejects hidden clock behavior
// and pins source inclusivity at exact start/end boundaries.
func TestTranslateItemQueryScalarClockBoundaries(t *testing.T) {
	trueValue, falseValue := true, false
	at := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name  string
		query store.ItemQuery
		want  string
		args  []any
	}{
		{"airing includes both exact endpoints", store.ItemQuery{IsAiring: &trueValue, ReferenceTime: &at}, "start_date <=", sqliteAiringArgs(at)},
		{"not airing uses source's strict outside conjunction", store.ItemQuery{IsAiring: &falseValue, ReferenceTime: &at}, "start_date >", []any{sqlite.Dialect{}.TimeValue(at), sqlite.Dialect{}.TimeValue(at)}},
		{"has aired includes the end boundary", store.ItemQuery{HasAired: &trueValue, ReferenceTime: &at}, "end_date <=", []any{sqlite.Dialect{}.TimeValue(at)}},
		{"not aired excludes the end boundary", store.ItemQuery{HasAired: &falseValue, ReferenceTime: &at}, "end_date >", []any{sqlite.Dialect{}.TimeValue(at)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			translated, err := store.Translate(tc.query, sqlite.Dialect{})
			if err != nil {
				t.Fatalf("Translate: %v", err)
			}
			if !strings.Contains(translated.SQL, tc.want) || !strings.Contains(translated.CountSQL, tc.want) {
				t.Fatalf("clock-bound SQL page=%q count=%q, want %q", translated.SQL, translated.CountSQL, tc.want)
			}
			if got, want := translated.CountArgs, tc.args; !reflect.DeepEqual(got, want) {
				t.Fatalf("clock-bound arguments = %#v, want %#v", got, want)
			}
		})
	}
	if _, err := store.Translate(store.ItemQuery{IsAiring: &trueValue}, sqlite.Dialect{}); err == nil || !strings.Contains(err.Error(), "reference") {
		t.Fatalf("Translate(IsAiring without reference) error = %v, want explicit reference-time rejection", err)
	}
}

// TestTranslateItemQueryOrdersEveryPinnedSort defends the SQL-visible mapping
// for the complete 30-value upstream enum. It deliberately tests every value,
// including previously supported SortName, so enum additions cannot be silently
// accepted with a SortName fallback. Sources: OrderMapper.cs:30-78 and
// QueryBuilding.cs:273-375 at b04614e; Deadfin additionally ends every order
// with id ASC for page stability.
func TestTranslateItemQueryOrdersEveryPinnedSort(t *testing.T) {
	userID := scalarOrderUUID(t, 700)
	cases := []struct {
		sort store.ItemSortBy
		want string
		user bool
	}{
		{store.ItemSortByDefault, "{sort_name} ASC NULLS FIRST, id ASC", false},
		{store.ItemSortByAiredEpisodeOrder, "{sort_name} ASC NULLS FIRST", false},
		{store.ItemSortByAlbum, "{album} ASC NULLS FIRST", false},
		{store.ItemSortByAlbumArtist, "item_values", false},
		{store.ItemSortByArtist, "item_values", false},
		{store.ItemSortByDateCreated, "date_created ASC NULLS FIRST", false},
		{store.ItemSortByOfficialRating, "inherited_parental_rating_value ASC NULLS FIRST", false},
		{store.ItemSortByDatePlayed, "item_user_data_keys", false},
		{store.ItemSortByPremiereDate, "COALESCE(premiere_date", false},
		{store.ItemSortByStartDate, "start_date ASC NULLS FIRST", false},
		{store.ItemSortBySortName, "{sort_name} ASC NULLS FIRST, {name} ASC NULLS FIRST", false},
		{store.ItemSortByName, "{sort_name} ASC NULLS FIRST", false},
		{store.ItemSortByRandom, "RANDOM(), id ASC", false},
		{store.ItemSortByRuntime, "run_time_ticks ASC NULLS FIRST", false},
		{store.ItemSortByCommunityRating, "community_rating ASC NULLS FIRST", false},
		{store.ItemSortByProductionYear, "production_year ASC NULLS FIRST", false},
		{store.ItemSortByPlayCount, "item_user_data_keys", true},
		{store.ItemSortByCriticRating, "critic_rating ASC NULLS FIRST", false},
		{store.ItemSortByIsFolder, "is_folder ASC NULLS FIRST", false},
		{store.ItemSortByIsUnplayed, "NOT COALESCE", true},
		{store.ItemSortByIsPlayed, "COALESCE", true},
		{store.ItemSortBySeriesSortName, "{series_name} ASC NULLS FIRST", false},
		{store.ItemSortByVideoBitRate, "total_bitrate ASC NULLS FIRST", false},
		{store.ItemSortByAirTime, "{sort_name} ASC NULLS FIRST", false},
		{store.ItemSortByStudio, "item_values", false},
		{store.ItemSortByIsFavoriteOrLiked, "item_user_data_keys", true},
		{store.ItemSortByDateLastContentAdded, "date_last_media_added ASC NULLS FIRST", false},
		{store.ItemSortBySeriesDatePlayed, "item_user_data_keys", false},
		{store.ItemSortByParentIndexNumber, "parent_index_number ASC NULLS FIRST", false},
		{store.ItemSortByIndexNumber, "index_number ASC NULLS FIRST", false},
	}
	if got, want := len(cases), len(store.ItemSortByValues()); got != want {
		t.Fatalf("sort ratchet cases = %d, want one for each pinned ItemSortBy value (%d)", got, want)
	}
	for _, tc := range cases {
		t.Run(string(tc.sort), func(t *testing.T) {
			query := store.ItemQuery{OrderBy: []store.ItemOrder{{SortBy: tc.sort, SortOrder: store.SortOrderAscending}}}
			if tc.user {
				query.UserID = &userID
			}
			for _, dialect := range scalarOrderDialects() {
				t.Run(dialect.name, func(t *testing.T) {
					translated, err := store.Translate(query, dialect.dialect)
					if err != nil {
						t.Fatalf("Translate(%s): %v", tc.sort, err)
					}
					want := scalarOrderCollationWant(tc.want, dialect.dialect)
					if !strings.Contains(translated.SQL, want) {
						t.Fatalf("ORDER BY for %s = %s\nwant source-pinned expression containing %q", tc.sort, translated.SQL, want)
					}
					if !strings.HasSuffix(translated.SQL, "id ASC") {
						t.Fatalf("ORDER BY for %s must finish with stable id ASC: %s", tc.sort, translated.SQL)
					}
					if strings.Contains(strings.ToUpper(translated.CountSQL), " ORDER BY ") {
						t.Fatalf("count query must precede ordering for %s: %s", tc.sort, translated.CountSQL)
					}
				})
			}
		})
	}
}

func TestTranslateItemQueryPinsBinaryTextCollationPerDialect(t *testing.T) {
	cases := []struct {
		name           string
		dialect        store.Dialect
		sortName       string
		nameTieBreak   string
		valueSort      string
		valueSelection string
	}{
		{
			name:           "SQLite",
			dialect:        sqlite.Dialect{},
			sortName:       "sort_name COLLATE BINARY",
			nameTieBreak:   "name COLLATE BINARY",
			valueSort:      "value.clean_value COLLATE BINARY",
			valueSelection: ") COLLATE BINARY",
		},
		{
			name:           "PostgreSQL",
			dialect:        postgres.Dialect{},
			sortName:       `sort_name COLLATE "C"`,
			nameTieBreak:   `name COLLATE "C"`,
			valueSort:      `value.clean_value COLLATE "C"`,
			valueSelection: `) COLLATE "C"`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			byName, err := store.Translate(store.ItemQuery{SortBy: []store.ItemSortBy{store.ItemSortBySortName}}, tc.dialect)
			if err != nil {
				t.Fatalf("Translate SortName: %v", err)
			}
			for _, want := range []string{tc.sortName + " ASC", tc.nameTieBreak + " ASC"} {
				if !strings.Contains(byName.SQL, want) {
					t.Fatalf("SortName SQL = %s, want pinned text order %q", byName.SQL, want)
				}
			}

			byArtist, err := store.Translate(store.ItemQuery{SortBy: []store.ItemSortBy{store.ItemSortByArtist}}, tc.dialect)
			if err != nil {
				t.Fatalf("Translate Artist: %v", err)
			}
			for _, want := range []string{tc.valueSort + " ASC", tc.valueSelection} {
				if !strings.Contains(byArtist.SQL, want) {
					t.Fatalf("Artist SQL = %s, want pinned text order %q", byArtist.SQL, want)
				}
			}
		})
	}
}

func TestTranslateItemQueryOrderDirectionsRelevanceAndPaging(t *testing.T) {
	userID := scalarOrderUUID(t, 701)
	limit, start := 2, 1
	t.Run("canonical multi sort respects explicit directions", func(t *testing.T) {
		translated, err := store.Translate(store.ItemQuery{OrderBy: []store.ItemOrder{{SortBy: store.ItemSortByRuntime, SortOrder: store.SortOrderDescending}, {SortBy: store.ItemSortByName, SortOrder: store.SortOrderDescending}}}, sqlite.Dialect{})
		if err != nil {
			t.Fatalf("Translate multi-sort: %v", err)
		}
		want := "run_time_ticks DESC NULLS LAST, " + (sqlite.Dialect{}).TextCollation("sort_name") + " DESC NULLS LAST"
		if !strings.Contains(translated.SQL, want) {
			t.Fatalf("multi-sort direction propagation = %s, want a descending direction on both supplied fields", translated.SQL)
		}
	})
	t.Run("explicit mixed directions retain positional pairing", func(t *testing.T) {
		translated, err := store.Translate(store.ItemQuery{OrderBy: []store.ItemOrder{{SortBy: store.ItemSortByRuntime, SortOrder: store.SortOrderDescending}, {SortBy: store.ItemSortByName, SortOrder: store.SortOrderAscending}}}, sqlite.Dialect{})
		if err != nil {
			t.Fatalf("Translate multi-sort: %v", err)
		}
		want := "run_time_ticks DESC NULLS LAST, " + (sqlite.Dialect{}).TextCollation("sort_name") + " ASC NULLS FIRST"
		if !strings.Contains(translated.SQL, want) {
			t.Fatalf("multi-sort positional directions = %s", translated.SQL)
		}
	})
	t.Run("too many directions fail instead of silently dropping one", func(t *testing.T) {
		_, err := store.Translate(store.ItemQuery{SortBy: []store.ItemSortBy{store.ItemSortByRuntime}, SortOrder: []store.SortOrder{store.SortOrderAscending, store.SortOrderDescending}}, sqlite.Dialect{})
		if err == nil || !strings.Contains(err.Error(), "sort orders") {
			t.Fatalf("Translate mismatched directions error = %v, want sort-order mismatch", err)
		}
	})
	t.Run("direction without field is ignored like RequestHelpers GetOrderBy", func(t *testing.T) {
		translated, err := store.Translate(store.ItemQuery{SortOrder: []store.SortOrder{store.SortOrderDescending}}, sqlite.Dialect{})
		if err != nil {
			t.Fatalf("Translate SortOrder without SortBy: %v", err)
		}
		want := "ORDER BY " + (sqlite.Dialect{}).TextCollation("sort_name") + " ASC NULLS FIRST, " + (sqlite.Dialect{}).TextCollation("name") + " ASC NULLS FIRST, id ASC"
		if !strings.Contains(translated.SQL, want) {
			t.Fatalf("SortOrder without SortBy order = %s, want default SortName ascending", translated.SQL)
		}
	})
	t.Run("search relevance is prepended before caller sort and count precedes paging", func(t *testing.T) {
		translated, err := store.Translate(store.ItemQuery{SearchTerm: "needle", OrderBy: []store.ItemOrder{{SortBy: store.ItemSortByPlayCount, SortOrder: store.SortOrderDescending}}, UserID: &userID, Limit: &limit, StartIndex: &start}, sqlite.Dialect{})
		if err != nil {
			t.Fatalf("Translate search sort: %v", err)
		}
		order := translated.SQL[strings.Index(translated.SQL, " ORDER BY "):]
		if !strings.HasPrefix(order, " ORDER BY CASE WHEN clean_name") || !strings.Contains(order, "COALESCE") || !strings.HasSuffix(order, "LIMIT ? OFFSET ?") {
			t.Fatalf("search order/page SQL = %s, want relevance then user sort then page", translated.SQL)
		}
		if strings.Contains(strings.ToUpper(translated.CountSQL), "ORDER BY") || strings.Contains(strings.ToUpper(translated.CountSQL), "LIMIT") {
			t.Fatalf("count SQL must be transformed before paging: %s", translated.CountSQL)
		}
		if got, want := translated.Args[len(translated.Args)-2:], []any{2, 1}; !reflect.DeepEqual(got, want) {
			t.Fatalf("page arguments = %#v, want limit/start %#v", got, want)
		}
	})
}

// TestTranslateItemQueryLegacySortByUsesLibraryManagerComparers keeps the
// legacy SortBy/SortOrder request surface distinct from canonical OrderBy.
// Sources: LibraryManager.cs:2239-2335 at b04614e.
func TestTranslateItemQueryLegacySortByUsesLibraryManagerComparers(t *testing.T) {
	cases := []struct {
		name      string
		sort      store.ItemSortBy
		direction store.SortOrder
		want      string
	}{
		{"aired episode order asc is sort name", store.ItemSortByAiredEpisodeOrder, store.SortOrderAscending, (sqlite.Dialect{}).TextCollation("sort_name") + " ASC NULLS FIRST"},
		{"aired episode order desc is sort name", store.ItemSortByAiredEpisodeOrder, store.SortOrderDescending, (sqlite.Dialect{}).TextCollation("sort_name") + " DESC NULLS LAST"},
		{"runtime treats absent runtime as zero", store.ItemSortByRuntime, store.SortOrderAscending, "COALESCE(run_time_ticks, 0) ASC NULLS FIRST"},
		{"production year falls back to premiere year then zero", store.ItemSortByProductionYear, store.SortOrderAscending, "COALESCE(production_year, CAST(strftime('%Y', premiere_date) AS INTEGER), 0) ASC NULLS FIRST"},
		{"folder asc keeps raw false-before-true column order", store.ItemSortByIsFolder, store.SortOrderAscending, "is_folder ASC NULLS FIRST"},
		{"folder desc keeps raw true-before-false column order", store.ItemSortByIsFolder, store.SortOrderDescending, "is_folder DESC NULLS LAST"},
		{"air time asc retains requested sort-name order", store.ItemSortByAirTime, store.SortOrderAscending, "ORDER BY " + (sqlite.Dialect{}).TextCollation("sort_name") + " ASC NULLS FIRST, id ASC"},
		{"air time desc retains requested sort-name order", store.ItemSortByAirTime, store.SortOrderDescending, "ORDER BY " + (sqlite.Dialect{}).TextCollation("sort_name") + " DESC NULLS LAST, id ASC"},
		{"last content added only has a folder timestamp", store.ItemSortByDateLastContentAdded, store.SortOrderAscending, "CASE WHEN is_folder THEN COALESCE(date_last_media_added"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			translated, err := store.Translate(store.ItemQuery{SortBy: []store.ItemSortBy{tc.sort}, SortOrder: []store.SortOrder{tc.direction}}, sqlite.Dialect{})
			if err != nil {
				t.Fatalf("translating legacy %s: %v", tc.sort, err)
			}
			if !strings.Contains(translated.SQL, tc.want) {
				t.Fatalf("legacy %s SQL = %s, want comparer expression %q", tc.sort, translated.SQL, tc.want)
			}
		})
	}
	t.Run("first supplied legacy direction broadcasts to later sort fields", func(t *testing.T) {
		translated, err := store.Translate(store.ItemQuery{SortBy: []store.ItemSortBy{store.ItemSortByRuntime, store.ItemSortByName}, SortOrder: []store.SortOrder{store.SortOrderDescending}}, sqlite.Dialect{})
		if err != nil {
			t.Fatalf("translating legacy broadcast: %v", err)
		}
		want := "COALESCE(run_time_ticks, 0) DESC NULLS LAST, " + (sqlite.Dialect{}).TextCollation("sort_name") + " DESC NULLS LAST"
		if !strings.Contains(translated.SQL, want) {
			t.Fatalf("legacy broadcast SQL = %s, want descending comparer directions", translated.SQL)
		}
	})
}

func scalarOrderCollationWant(want string, dialect store.Dialect) string {
	for _, column := range []string{"sort_name", "name", "album", "series_name"} {
		want = strings.ReplaceAll(want, "{"+column+"}", dialect.TextCollation(column))
	}
	return want
}

type scalarOrderDialect struct {
	name    string
	dialect store.Dialect
}

func scalarOrderDialects() []scalarOrderDialect {
	return []scalarOrderDialect{{"SQLite", sqlite.Dialect{}}, {"PostgreSQL", postgres.Dialect{}}}
}

func scalarOrderUUID(t *testing.T, value int) dto.UUID {
	t.Helper()
	id, err := dto.ParseUUID(fmt.Sprintf("%032x", value))
	if err != nil {
		t.Fatalf("parsing UUID %d: %v", value, err)
	}
	return id
}

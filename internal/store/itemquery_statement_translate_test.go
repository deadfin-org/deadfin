// SPDX-License-Identifier: GPL-2.0-only

package store_test

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/postgres"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
)

// TestTranslateItemQueryCTEStatementsAreSingleAndSeparate keeps CTE-based
// query translation safe for the two Query calls: page and count remain
// separate single statements, while search results union with existing caller IDs.
func TestTranslateItemQueryCTEStatementsAreSingleAndSeparate(t *testing.T) {
	firstID := statementTranslateUUID(t, 701)
	secondID := statementTranslateUUID(t, 702)
	limit, startIndex := 3, 5
	searchTerm := "bound; search value"
	query := store.ItemQuery{
		ItemIDs:                []dto.UUID{firstID, secondID},
		SearchTerm:             searchTerm,
		EnableTotalRecordCount: true,
		Limit:                  &limit,
		StartIndex:             &startIndex,
	}

	cases := []struct {
		name             string
		dialect          store.Dialect
		placeholder      string
		otherPlaceholder string
	}{
		{name: "SQLite", dialect: sqlite.Dialect{}, placeholder: "?", otherPlaceholder: "$1"},
		{name: "PostgreSQL", dialect: postgres.Dialect{}, placeholder: "$1", otherPlaceholder: "?"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			translated, err := store.Translate(query, tc.dialect)
			if err != nil {
				t.Fatalf("Translate: %v", err)
			}

			for _, statement := range []struct {
				name string
				sql  string
			}{
				{name: "page", sql: translated.SQL},
				{name: "count", sql: translated.CountSQL},
			} {
				if strings.Contains(statement.sql, ";") {
					t.Fatalf("%s SQL contains a statement separator: %q", statement.name, statement.sql)
				}
				if !strings.HasPrefix(strings.TrimSpace(statement.sql), "WITH ") {
					t.Fatalf("%s SQL = %q, want one CTE statement", statement.name, statement.sql)
				}
				for _, cte := range []string{
					"filtered_ids AS (",
					"explicit_item_ids(id) AS (VALUES ",
					"accessible_explicit_item_ids AS (SELECT explicit_item.id FROM items AS explicit_item JOIN explicit_item_ids AS requested ON requested.id = explicit_item.id)",
					"search_or_explicit_ids AS (",
					"UNION SELECT id FROM accessible_explicit_item_ids",
				} {
					if !strings.Contains(statement.sql, cte) {
						t.Fatalf("%s SQL = %q, want CTE fragment %q", statement.name, statement.sql, cte)
					}
				}
				if strings.Contains(statement.sql, searchTerm) || strings.Contains(statement.sql, firstID.Dashed()) || strings.Contains(statement.sql, secondID.Dashed()) {
					t.Fatalf("%s SQL interpolated caller-bound search or ID value: %q", statement.name, statement.sql)
				}
				if !strings.Contains(statement.sql, tc.placeholder) || strings.Contains(statement.sql, tc.otherPlaceholder) {
					t.Fatalf("%s SQL = %q, want only %s placeholder convention", statement.name, statement.sql, tc.placeholder)
				}
			}

			if !strings.Contains(translated.SQL, "WHERE items.id IN (SELECT id FROM search_or_explicit_ids)") {
				t.Fatalf("page SQL = %q, want page materialization from the transformed ID relation", translated.SQL)
			}
			if !strings.Contains(translated.CountSQL, "SELECT COUNT(*) FROM search_or_explicit_ids") {
				t.Fatalf("count SQL = %q, want unpaged count from the transformed ID relation", translated.CountSQL)
			}
			if strings.Contains(strings.ToUpper(translated.CountSQL), " ORDER BY ") || strings.Contains(strings.ToUpper(translated.CountSQL), " LIMIT ") || strings.Contains(strings.ToUpper(translated.CountSQL), " OFFSET ") {
				t.Fatalf("count SQL must remain unpaged: %q", translated.CountSQL)
			}
			// Six search bindings: clean_name, original_title, and the anime
			// clean_title and fold_title, once in the EXISTS guard and once in
			// the IN subquery.
			if got, want := len(translated.CountArgs), 8; got != want {
				t.Fatalf("count arguments = %#v, want six search bindings and two explicit IDs", translated.CountArgs)
			}
			if len(translated.Args) < len(translated.CountArgs)+2 || !reflect.DeepEqual(translated.Args[:len(translated.CountArgs)], translated.CountArgs) {
				t.Fatalf("page arguments = %#v, want count arguments %#v as the prefix", translated.Args, translated.CountArgs)
			}
			if got, want := translated.Args[len(translated.Args)-2:], []any{limit, startIndex}; !reflect.DeepEqual(got, want) {
				t.Fatalf("page pagination arguments = %#v, want %#v", got, want)
			}
		})
	}
}

func statementTranslateUUID(t *testing.T, value int) dto.UUID {
	t.Helper()
	id, err := dto.ParseUUID(fmt.Sprintf("%032x", value))
	if err != nil {
		t.Fatalf("parsing statement UUID %d: %v", value, err)
	}
	return id
}

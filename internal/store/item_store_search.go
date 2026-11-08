// SPDX-License-Identifier: GPL-2.0-only

package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
)

// The SqlSearchProvider match scores
// (upstream/jellyfin/Emby.Server.Implementations/Library/Search/SqlSearchProvider.cs:25-29).
const (
	SearchScoreExact      float32 = 100
	SearchScorePrefix     float32 = 80
	SearchScoreWordPrefix float32 = 75
	SearchScoreContains   float32 = 50
)

// SearchScore is one search candidate and its match score.
type SearchScore struct {
	ID    dto.UUID
	Score float32
}

// SearchScores ports SqlSearchProvider.SearchAsync: it returns the items that
// query matches, scored by how their clean name matches query.SearchTerm,
// highest score first, then by id, at most limit of them. The query must
// carry the search term; its filters select the candidates exactly as an
// Items search does, including the anime title search. A name equal to the
// term scores 100, a name that starts with it 80, a name that contains the
// term followed by a space 75, and any other match 50.
func (s *ItemStore) SearchScores(ctx context.Context, query ItemQuery, limit int) (_ []SearchScore, err error) {
	clean := item.CleanName(query.SearchTerm)
	if clean == "" || limit <= 0 {
		return nil, nil
	}
	query.StartIndex, query.Limit, query.SortBy, query.SortOrder = nil, nil, nil, nil
	tx, err := s.stores.Reader().BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer rollbackOnReturn(tx, &err)
	_, translated, err := s.translateQuery(ctx, tx, query)
	if err != nil {
		return nil, err
	}
	// The score comes before the id relation in the statement. SQLite binds
	// "?" by position, so its arguments go first there; PostgreSQL numbers
	// them after the relation's arguments.
	d := s.stores.Dialect()
	base := len(translated.CountArgs)
	var scoreArgs []any
	arg := func(value any) string {
		scoreArgs = append(scoreArgs, value)
		return s.ph(base + len(scoreArgs))
	}
	escaped := d.EscapeLike(clean)
	score := fmt.Sprintf("CASE WHEN items.clean_name = %s THEN %v WHEN %s THEN %v WHEN %s THEN %v ELSE %v END",
		arg(clean), SearchScoreExact,
		d.SearchClause("items.clean_name", arg(escaped+"%")), SearchScorePrefix,
		d.SearchClause("items.clean_name", arg("%"+escaped+" %")), SearchScoreWordPrefix,
		SearchScoreContains)
	args := append(append([]any(nil), translated.CountArgs...), scoreArgs...)
	if d.Placeholder(1) == "?" {
		args = append(append([]any(nil), scoreArgs...), translated.CountArgs...)
	}
	args = append(args, limit)
	// When a WHERE clause over items selects the candidates, the score reads
	// the rows that the filter reads. A join back to items by id reads the
	// full row of every candidate, which costs most of a short term's search,
	// as such a term matches much of the guide (deadfin#885).
	//
	// With a visibility filter, the backends differ. PostgreSQL cannot
	// estimate the search filter, so behind the visible_ids relation it
	// hashes the visibility test of every item; one scan with both filters
	// avoids that. SQLite keeps the join: the visibility filter reads the
	// full rows, so one scan reads the table instead of the search index,
	// and a search with few matches gets slower.
	from := "(" + translated.IDSQL + ") AS scope CROSS JOIN items WHERE items.id = scope.id"
	if translated.FilteredFrom == "items" || (translated.Direct && d.Placeholder(1) != "?") {
		from = "items" + translated.DirectWhere
	}
	statement := "SELECT items.id, " + score + " AS score FROM " + from + " ORDER BY score DESC, items.id ASC LIMIT " + s.ph(len(args))
	rows, err := tx.QueryContext(ctx, statement, args...)
	if err != nil {
		return nil, err
	}
	var out []SearchScore
	for rows.Next() {
		var raw any
		var value float64
		if err := rows.Scan(&raw, &value); err != nil {
			return nil, closeRows(rows, err)
		}
		id, err := scanUUID(raw)
		if err != nil {
			return nil, closeRows(rows, err)
		}
		out = append(out, SearchScore{ID: id, Score: float32(value)})
	}
	if err := closeRows(rows, rows.Err()); err != nil {
		return nil, err
	}
	return out, tx.Commit()
}

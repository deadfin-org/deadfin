// SPDX-License-Identifier: GPL-2.0-only

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
)

// Query returns the requested stable item page. It counts the filtered set
// before pagination when requested and loads the same satellite data as Get.
func (s *ItemStore) Query(ctx context.Context, query ItemQuery) (result ItemQueryResult, err error) {
	tx, err := s.stores.Reader().BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return ItemQueryResult{}, err
	}
	defer rollbackOnReturn(tx, &err)

	query, translated, err := s.translateQuery(ctx, tx, query)
	if err != nil {
		return ItemQueryResult{}, err
	}

	if query.EnableTotalRecordCount {
		if err := tx.QueryRowContext(ctx, translated.CountSQL, translated.CountArgs...).Scan(&result.TotalRecordCount); err != nil {
			return ItemQueryResult{}, err
		}
	}

	rows, err := tx.QueryContext(ctx, translated.SQL, translated.Args...)
	if err != nil {
		return ItemQueryResult{}, err
	}
	for rows.Next() {
		it, err := scanItem(rows, s.stores.Dialect())
		if err != nil {
			return ItemQueryResult{}, closeRows(rows, err)
		}
		result.Items = append(result.Items, it)
	}
	if err := closeRows(rows, rows.Err()); err != nil {
		return ItemQueryResult{}, err
	}

	if err := s.loadPageSatellites(ctx, tx, result.Items); err != nil {
		return ItemQueryResult{}, err
	}
	if !query.EnableTotalRecordCount {
		result.TotalRecordCount = len(result.Items)
	}
	if err := tx.Commit(); err != nil {
		return ItemQueryResult{}, err
	}
	return result, nil
}

// TranslateQuery returns the statements that Query runs for query. It reads
// the parent facts in a reader transaction as Query does, so a plan check
// sees the production statements, which Translate alone cannot produce.
func (s *ItemStore) TranslateQuery(ctx context.Context, query ItemQuery) (_ TranslatedItemQuery, err error) {
	tx, err := s.stores.Reader().BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return TranslatedItemQuery{}, err
	}
	defer rollbackOnReturn(tx, &err)
	_, translated, err := s.translateQuery(ctx, tx, query)
	if err != nil {
		return TranslatedItemQuery{}, err
	}
	return translated, tx.Commit()
}

// translateQuery resolves the parent scope in the reader transaction and
// translates the query.
func (s *ItemStore) translateQuery(ctx context.Context, tx *sql.Tx, query ItemQuery) (ItemQuery, TranslatedItemQuery, error) {
	if query.ParentID != nil && !query.ParentID.IsNil() && len(query.flattenRootFolderIDs) == 0 {
		parent, err := s.queryParent(ctx, tx, *query.ParentID)
		if err != nil {
			return query, TranslatedItemQuery{}, err
		}
		if len(parent.physicalFolderIDs) != 0 {
			query.flattenRootFolderIDs = parent.physicalFolderIDs
		}
		query.recursiveParent = &recursiveParentFacts{kind: parent.kind, topParent: parent.topParent}
		applyLinkedParentScope(&query, parent.kind)
		applySeasonParentScope(&query, parent.kind)
		applyMusicAlbumParentScope(&query, parent.kind)
	}
	translated, err := Translate(query, s.stores.Dialect())
	return query, translated, err
}

// queryParentFacts are the stored facts of a query's ParentID row.
type queryParentFacts struct {
	// kind is empty when the parent does not exist.
	kind string
	// physicalFolderIDs are the physical root Folder IDs on a
	// CollectionFolder's extra. They drive the non-recursive flatten clause
	// and the recursive exclusion. They are empty for other kinds and for a
	// CollectionFolder without the marker, so a not-yet-scanned legacy
	// database browses exactly as before.
	physicalFolderIDs []dto.UUID
	// topParent is true when the parent is its own top parent: it has no
	// parent, or its parent is the AggregateFolder. The derived top_parent_id
	// of every descendant is then the parent itself (derivedHierarchy).
	topParent bool
	// collectionType is the CollectionFolder collection type or the UserView
	// view type, when the parent has one.
	collectionType *item.CollectionType
}

// queryParent reads the parent row in the page's reader transaction, so the
// facts and the page come from one snapshot. A reader transaction sees every
// committed scan write, and it never waits for the writer that a running scan
// holds. The read selects only the kind, extra, and parent kind columns and
// loads no satellites.
func (s *ItemStore) queryParent(ctx context.Context, tx *sql.Tx, parentID dto.UUID) (queryParentFacts, error) {
	var facts queryParentFacts
	var extraJSON []byte
	var grandparentID any
	var grandparentKind sql.NullString
	err := tx.QueryRowContext(ctx,
		"SELECT items.kind, items.extra, items.parent_id, grandparent.kind FROM items LEFT JOIN items AS grandparent ON grandparent.id = items.parent_id WHERE items.id = "+s.ph(1), s.uuidArg(parentID),
	).Scan(&facts.kind, &extraJSON, &grandparentID, &grandparentKind)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return queryParentFacts{}, nil
		}
		return queryParentFacts{}, err
	}
	facts.topParent = grandparentID == nil || (grandparentKind.Valid && grandparentKind.String == item.KindAggregateFolder.String())
	if facts.kind != item.KindCollectionFolder.String() && facts.kind != item.KindUserView.String() {
		return facts, nil
	}
	var extra item.ExtraFields
	if err := json.Unmarshal(extraJSON, &extra); err != nil {
		return queryParentFacts{}, err
	}
	if facts.kind == item.KindCollectionFolder.String() && extra.CollectionFolder != nil {
		facts.physicalFolderIDs = extra.CollectionFolder.PhysicalFolderIDs
		facts.collectionType = extra.CollectionFolder.CollectionType
	}
	if facts.kind == item.KindUserView.String() && extra.UserView != nil {
		facts.collectionType = extra.UserView.ViewType
	}
	return facts, nil
}

// applyLinkedParentScope ports Folder.GetChildren for the two folders whose
// children are linked items: a direct (non-recursive) parent query of a
// Playlist or BoxSet returns its linked children. A Playlist returns only
// non-folders (Playlist.GetPlayableItems) and, without an explicit sort, keeps
// its entry order (Playlist.IsPreSorted).
func applyLinkedParentScope(query *ItemQuery, parentKind string) {
	if query.Recursive {
		return
	}
	switch parentKind {
	case item.KindBoxSet.String():
		query.Recursive = true
	case item.KindPlaylist.String():
		query.Recursive = true
		if query.IsFolder == nil {
			folder := false
			query.IsFolder = &folder
		}
		if len(query.SortBy) == 0 && len(query.OrderBy) == 0 {
			parentID := *query.ParentID
			query.linkedOrderParentID = &parentID
		}
	}
}

// applySeasonParentScope ports Season.GetItemsInternal: a user's query
// with a Season parent, recursive or not, lists the season's episodes
// (Season.GetEpisodes), then applies the query's own filters, sort and page.
// Without a user, upstream keeps the base Folder scope. Explicit item IDs
// bypass GetItemsInternal (Folder.GetItems).
func applySeasonParentScope(query *ItemQuery, parentKind string) {
	if parentKind != item.KindSeason.String() || query.UserID == nil || query.UserID.IsNil() || len(query.ItemIDs) != 0 {
		return
	}
	seasonID := *query.ParentID
	query.EpisodesOfSeasonID = &seasonID
	query.ParentID = nil
	query.Recursive = false
	query.recursiveParent = nil
}

// applyMusicAlbumParentScope ports the MusicAlbum branch of
// Folder.GetItemsInternal: a user's direct (non-recursive) query of a
// MusicAlbum lists the album's tracks (Folder.AddChildren calls
// MusicAlbum.GetEligibleChildrenForRecursiveChildren, which returns
// Tracks, the recursive Audio children). The tracks of a multi-disc album
// live in disc folders ("CD 01"), so a direct parent scope misses them.
// DisplayAlbumFolders keeps the direct children. Without a user, upstream
// keeps the base Folder scope. Explicit item IDs bypass GetItemsInternal.
func applyMusicAlbumParentScope(query *ItemQuery, parentKind string) {
	if parentKind != item.KindMusicAlbum.String() || query.Recursive || query.UserID == nil || query.UserID.IsNil() || len(query.ItemIDs) != 0 || query.DisplayAlbumFolders != nil && *query.DisplayAlbumFolders {
		return
	}
	query.Recursive = true
	if len(query.IncludeItemTypes) == 0 || slices.Contains(query.IncludeItemTypes, item.KindAudio) {
		query.IncludeItemTypes = []item.Kind{item.KindAudio}
		return
	}
	// The query filters only the tracks, so a type filter without Audio
	// selects nothing. No Audio item is a folder.
	query.IncludeItemTypes = []item.Kind{item.KindAudio}
	isFolder := true
	query.IsFolder = &isFolder
}

// ItemIDsByCleanName returns the IDs of items of the given kinds whose
// clean_name is one of cleanNames. It reads ix_items_clean_name and loads no
// item rows, so a caller can resolve a few named entities without reading
// every entity of a kind. The order of the result is not defined.
func (s *ItemStore) ItemIDsByCleanName(ctx context.Context, kinds []item.Kind, cleanNames []string) ([]dto.UUID, error) {
	if len(kinds) == 0 || len(cleanNames) == 0 {
		return nil, nil
	}
	const chunk = 500
	var ids []dto.UUID
	for start := 0; start < len(cleanNames); start += chunk {
		names := cleanNames[start:min(start+chunk, len(cleanNames))]
		args := make([]any, 0, len(names)+len(kinds))
		marks := func(values []any) string {
			list := make([]string, len(values))
			for index, value := range values {
				args = append(args, value)
				list[index] = s.ph(len(args))
			}
			return strings.Join(list, ", ")
		}
		nameArgs := make([]any, len(names))
		for index, name := range names {
			nameArgs[index] = name
		}
		kindArgs := make([]any, len(kinds))
		for index, kind := range kinds {
			kindArgs[index] = kind.String()
		}
		query := "SELECT id FROM items WHERE clean_name IN (" + marks(nameArgs) + ") AND kind IN (" + marks(kindArgs) + ")"
		rows, err := s.stores.Reader().QueryContext(ctx, query, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var raw any
			if err := rows.Scan(&raw); err != nil {
				return nil, closeRows(rows, err)
			}
			id, err := scanUUID(raw)
			if err != nil {
				return nil, closeRows(rows, err)
			}
			ids = append(ids, id)
		}
		if err := closeRows(rows, rows.Err()); err != nil {
			return nil, err
		}
	}
	return ids, nil
}

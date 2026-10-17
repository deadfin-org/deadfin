// SPDX-License-Identifier: GPL-2.0-only

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/playback"
)

// MatchUserDataRow returns the first persisted user-data row in the item's
// canonical key order. When no row is present, key remains the first canonical
// key so callers that project an absent row retain its compatible key. An
// episode's series must be in inputs: its first keys derive from the series
// keys, and the write path stores the episode's row under the first one.
func MatchUserDataRow(value item.Item, inputs item.UserDataInputs, rows map[string]UserData) (key string, data UserData, found bool) {
	keys := item.UserDataKeys(value, inputs)
	if len(keys) != 0 {
		key = keys[0]
	}
	for _, candidate := range keys {
		if data, found = rows[candidate]; found {
			return candidate, data, true
		}
	}
	return key, UserData{}, false
}

// VersionUserData reads user data for a set of item versions in two batched
// store reads. The result contains only items with a canonical persisted row.
func (s *ItemStore) VersionUserData(ctx context.Context, userID dto.UUID, ids []dto.UUID) (map[dto.UUID]UserData, error) {
	unique := deduplicatePlaybackSelectionIDs(ids)
	result := make(map[dto.UUID]UserData, len(unique))
	if len(unique) == 0 {
		return result, nil
	}

	page, err := s.Query(ctx, ItemQuery{ItemIDs: unique, IncludeOwnedItems: true})
	if err != nil {
		return nil, err
	}
	projections, err := s.DTOProjections(ctx, unique, &userID, false, true, false, false, nil)
	if err != nil {
		return nil, err
	}
	series, err := s.userDataSeries(ctx, page.Items)
	if err != nil {
		return nil, err
	}
	for _, value := range page.Items {
		_, data, found := MatchUserDataRow(value, UserDataInputsFor(value, series), projections.UserData[value.ID])
		if found {
			result[value.ID] = data
		}
	}
	return result, nil
}

// UserDataInputsFor returns the key inputs for value from a set of loaded
// series items.
func UserDataInputsFor(value item.Item, series map[dto.UUID]item.Item) item.UserDataInputs {
	if value.SeriesID == nil {
		return item.UserDataInputs{}
	}
	if related, found := series[*value.SeriesID]; found {
		return item.UserDataInputs{Series: &related}
	}
	return item.UserDataInputs{}
}

// userDataSeries loads the series of every episode or season in values.
func (s *ItemStore) userDataSeries(ctx context.Context, values []item.Item) (map[dto.UUID]item.Item, error) {
	ids := make([]dto.UUID, 0)
	seen := make(map[dto.UUID]struct{})
	for _, value := range values {
		if value.SeriesID == nil || value.Kind != item.KindEpisode && value.Kind != item.KindSeason {
			continue
		}
		if _, found := seen[*value.SeriesID]; !found {
			seen[*value.SeriesID] = struct{}{}
			ids = append(ids, *value.SeriesID)
		}
	}
	series := make(map[dto.UUID]item.Item, len(ids))
	if len(ids) == 0 {
		return series, nil
	}
	page, err := s.Query(ctx, ItemQuery{ItemIDs: ids, IncludeItemTypes: []item.Kind{item.KindSeries}})
	if err != nil {
		return nil, err
	}
	for _, value := range page.Items {
		series[value.ID] = value
	}
	return series, nil
}

func deduplicatePlaybackSelectionIDs(ids []dto.UUID) []dto.UUID {
	if len(ids) < 2 {
		return ids
	}
	seen := make(map[dto.UUID]struct{}, len(ids))
	result := make([]dto.UUID, 0, len(ids))
	for _, id := range ids {
		if _, found := seen[id]; !found {
			seen[id] = struct{}{}
			result = append(result, id)
		}
	}
	return result
}

// InheritedOriginalLanguages resolves OriginalLanguage for each requested item
// through Jellyfin's ownership and series relationships. Relationship layers
// are fetched in batches, while the visited set terminates malformed cycles.
func (s *ItemStore) InheritedOriginalLanguages(ctx context.Context, values []item.Item) (map[dto.UUID]string, error) {
	result := make(map[dto.UUID]string, len(values))
	known := make(map[dto.UUID]item.Item, len(values))
	frontier := make([]item.Item, 0, len(values))
	visited := make(map[dto.UUID]struct{}, len(values))
	for _, value := range values {
		result[value.ID] = ""
		if _, found := visited[value.ID]; found {
			continue
		}
		visited[value.ID] = struct{}{}
		known[value.ID] = value
		frontier = append(frontier, value)
	}

	for len(frontier) != 0 {
		ids := make([]dto.UUID, 0, len(frontier))
		for _, value := range frontier {
			if value.OriginalLanguage != nil {
				continue
			}
			parentID := inheritedOriginalLanguageParent(value)
			if parentID == nil || parentID.IsNil() {
				continue
			}
			if _, found := visited[*parentID]; found {
				continue
			}
			visited[*parentID] = struct{}{}
			ids = append(ids, *parentID)
		}
		if len(ids) == 0 {
			break
		}

		page, err := s.Query(ctx, ItemQuery{ItemIDs: ids, IncludeOwnedItems: true})
		if err != nil {
			return nil, err
		}
		frontier = page.Items
		for _, value := range frontier {
			known[value.ID] = value
		}
	}

	for targetID := range result {
		traversed := make(map[dto.UUID]struct{})
		for currentID := targetID; ; {
			if _, found := traversed[currentID]; found {
				break
			}
			traversed[currentID] = struct{}{}
			value, found := known[currentID]
			if !found {
				break
			}
			if value.OriginalLanguage != nil {
				result[targetID] = *value.OriginalLanguage
				break
			}
			parentID := inheritedOriginalLanguageParent(value)
			if parentID == nil || parentID.IsNil() {
				break
			}
			currentID = *parentID
		}
	}
	return result, nil
}

func inheritedOriginalLanguageParent(value item.Item) *dto.UUID {
	switch value.Kind {
	case item.KindEpisode, item.KindSeason:
		return value.SeriesID
	}
	if item.MediaTypeOf(value) != item.MediaTypeVideo {
		return nil
	}
	if value.PrimaryVersionID != nil && !value.PrimaryVersionID.IsNil() {
		return value.PrimaryVersionID
	}
	return value.OwnerID
}

// SeriesStreamSelections returns, for each requested series, the stream
// indexes saved on the user's most recently played episode of that series that
// has a saved index, together with that episode's streams. Series without such
// an episode are absent from the result.
func (s *ItemStore) SeriesStreamSelections(ctx context.Context, userID dto.UUID, seriesIDs []dto.UUID) (result map[dto.UUID]playback.SeriesStreamSelection, err error) {
	result = make(map[dto.UUID]playback.SeriesStreamSelection)
	seriesIDs = deduplicatePlaybackSelectionIDs(seriesIDs)
	if len(seriesIDs) == 0 {
		return result, nil
	}
	tx, err := s.stores.Reader().BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer rollbackOnReturn(tx, &err)

	type latest struct {
		itemID     dto.UUID
		lastPlayed time.Time
		selection  playback.SeriesStreamSelection
	}
	chosen := make(map[dto.UUID]latest, len(seriesIDs))
	batchSize := pageSatelliteBatchSize - 2 // user_id, episode kind
	for start := 0; start < len(seriesIDs); start += batchSize {
		end := min(start+batchSize, len(seriesIDs))
		args := []any{s.uuidArg(userID), item.KindEpisode.String()}
		marks := make([]string, 0, end-start)
		for _, id := range seriesIDs[start:end] {
			args = append(args, s.uuidArg(id))
			marks = append(marks, s.ph(len(args)))
		}
		rows, queryErr := tx.QueryContext(ctx, "SELECT items.series_id,user_data.item_id,user_data.last_played,user_data.audio_stream_index,user_data.subtitle_stream_index FROM user_data JOIN items ON items.id = user_data.item_id WHERE user_data.user_id = "+s.ph(1)+" AND items.kind = "+s.ph(2)+" AND items.series_id IN ("+strings.Join(marks, ", ")+") AND user_data.last_played IS NOT NULL AND (user_data.audio_stream_index IS NOT NULL OR user_data.subtitle_stream_index IS NOT NULL)", args...)
		if queryErr != nil {
			return nil, queryErr
		}
		for rows.Next() {
			var rawSeries, rawItem, rawLastPlayed, audio, subtitle any
			if err := rows.Scan(&rawSeries, &rawItem, &rawLastPlayed, &audio, &subtitle); err != nil {
				return nil, closeRows(rows, err)
			}
			seriesID, err := scanUUID(rawSeries)
			if err != nil {
				return nil, closeRows(rows, err)
			}
			itemID, err := scanUUID(rawItem)
			if err != nil {
				return nil, closeRows(rows, err)
			}
			lastPlayed, err := s.stores.Dialect().ScanTime(rawLastPlayed)
			if err != nil {
				return nil, closeRows(rows, err)
			}
			current, found := chosen[seriesID]
			if found && (lastPlayed.Before(current.lastPlayed) || (lastPlayed.Equal(current.lastPlayed) && itemID.String() < current.itemID.String())) {
				continue
			}
			chosen[seriesID] = latest{itemID: itemID, lastPlayed: lastPlayed, selection: playback.SeriesStreamSelection{AudioStreamIndex: optionalInt(audio), SubtitleStreamIndex: optionalInt(subtitle)}}
		}
		if err := closeRows(rows, rows.Err()); err != nil {
			return nil, err
		}
	}
	if len(chosen) == 0 {
		return result, tx.Commit()
	}

	itemIDs := make([]dto.UUID, 0, len(chosen))
	for _, value := range chosen {
		itemIDs = append(itemIDs, value.itemID)
	}
	streams := make(map[dto.UUID][]playback.MediaStream, len(itemIDs))
	for start := 0; start < len(itemIDs); start += pageSatelliteBatchSize {
		end := min(start+pageSatelliteBatchSize, len(itemIDs))
		if err := s.dtoProjectionStreams(ctx, tx, itemIDs[start:end], streams); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	for seriesID, value := range chosen {
		value.selection.Streams = streams[value.itemID]
		result[seriesID] = value.selection
	}
	return result, nil
}

type playbackSelectionUserReader interface {
	Get(context.Context, dto.UUID) (User, error)
}

// PlaybackSelectionReader adapts store-backed item and user state to the pure
// playback media-source selection seam.
type PlaybackSelectionReader struct {
	items *ItemStore
	users playbackSelectionUserReader
}

func NewPlaybackSelectionReader(items *ItemStore, users playbackSelectionUserReader) PlaybackSelectionReader {
	return PlaybackSelectionReader{items: items, users: users}
}

// SourceSelection returns the selected user's source preferences and canonical
// playback state for the requested, access-filtered source version IDs.
func (reader PlaybackSelectionReader) SourceSelection(ctx context.Context, userID, itemID dto.UUID, versionIDs []dto.UUID) (playback.SourceSelection, bool, error) {
	user, err := reader.users.Get(ctx, userID)
	if err != nil {
		if errors.Is(err, ErrNotFound) || IsNotFound(err) {
			return playback.SourceSelection{}, false, nil
		}
		return playback.SourceSelection{}, false, err
	}

	configuration := dto.DefaultUserConfiguration()
	if len(user.Configuration) != 0 {
		if err := json.Unmarshal(user.Configuration, &configuration); err != nil {
			return playback.SourceSelection{}, false, err
		}
	}

	value, err := reader.items.Get(ctx, itemID)
	if err != nil {
		if errors.Is(err, ErrNotFound) || IsNotFound(err) {
			return playback.SourceSelection{}, false, nil
		}
		return playback.SourceSelection{}, false, err
	}
	rows, err := reader.items.VersionUserData(ctx, userID, versionIDs)
	if err != nil {
		return playback.SourceSelection{}, false, err
	}
	languages, err := reader.items.InheritedOriginalLanguages(ctx, []item.Item{value})
	if err != nil {
		return playback.SourceSelection{}, false, err
	}

	selection := playback.SourceSelection{
		Configuration:    configuration,
		MediaKind:        playbackSelectionMediaKind(value),
		OriginalLanguage: languages[itemID],
		PrimaryItem:      value.PrimaryVersionID == nil,
		VersionUserData:  make(map[string]playback.SourceUserData, len(rows)),
	}
	if seriesID, found := PlaybackSelectionSeriesID(value, configuration); found {
		series, err := reader.items.SeriesStreamSelections(ctx, userID, []dto.UUID{seriesID})
		if err != nil {
			return playback.SourceSelection{}, false, err
		}
		if previous, found := series[seriesID]; found {
			selection.SeriesSelection = &previous
		}
	}
	for id, data := range rows {
		selectionData := playbackSelectionUserData(data)
		selection.VersionUserData[id.String()] = selectionData
		if id == itemID {
			itemData := selectionData
			selection.ItemUserData = &itemData
		}
	}
	return selection, true, nil
}

// PlaybackSelectionSeriesID returns the series whose remembered stream choice
// applies to an episode. It reports false when the user remembers neither
// audio nor subtitle selections.
func PlaybackSelectionSeriesID(value item.Item, configuration dto.UserConfiguration) (dto.UUID, bool) {
	if value.Kind != item.KindEpisode || value.SeriesID == nil || value.SeriesID.IsNil() {
		return dto.UUID{}, false
	}
	if !configuration.RememberAudioSelections && !configuration.RememberSubtitleSelections {
		return dto.UUID{}, false
	}
	return *value.SeriesID, true
}

func playbackSelectionMediaKind(value item.Item) playback.SourceMediaKind {
	switch item.MediaTypeOf(value) {
	case item.MediaTypeVideo:
		return playback.SourceMediaVideo
	case item.MediaTypeAudio:
		return playback.SourceMediaAudio
	default:
		return playback.SourceMediaOther
	}
}

func playbackSelectionUserData(data UserData) playback.SourceUserData {
	result := playback.SourceUserData{PositionTicks: data.PositionTicks}
	if data.AudioStreamIndex != nil {
		value := *data.AudioStreamIndex
		result.AudioStreamIndex = &value
	}
	if data.SubtitleStreamIndex != nil {
		value := *data.SubtitleStreamIndex
		result.SubtitleStreamIndex = &value
	}
	if data.LastPlayed != nil {
		value := *data.LastPlayed
		result.LastPlayed = &value
	}
	return result
}

// SPDX-License-Identifier: GPL-2.0-only

package handlers

import (
	"context"
	"net/http"
	"slices"
	"sort"
	"strings"

	"github.com/deadfin-org/deadfin/internal/api/bind"
	"github.com/deadfin-org/deadfin/internal/api/render"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
)

// ItemStreamLanguageReader reads the distinct media-stream languages of the
// items a query matches. GET /Items/Filters2 uses it when the item reader
// implements it; otherwise the language lists stay empty.
type ItemStreamLanguageReader interface {
	QueryStreamLanguages(ctx context.Context, query store.ItemQuery, streamType string) ([]string, error)
}

// LanguageInfoFinder resolves a stream language to its culture, as
// LocalizationManager.FindLanguageInfo does.
type LanguageInfoFinder interface {
	FindLanguageInfo(language string) (dto.CultureDto, bool)
}

type queryFiltersRequest struct {
	IsAiring  *bool `query:"IsAiring"`
	IsMovie   *bool `query:"IsMovie"`
	IsSports  *bool `query:"IsSports"`
	IsKids    *bool `query:"IsKids"`
	IsNews    *bool `query:"IsNews"`
	IsSeries  *bool `query:"IsSeries"`
	Recursive *bool `query:"Recursive"`
}

// queryFilters ports FilterController.GetQueryFilters
// (upstream/jellyfin/Jellyfin.Api/Controllers/FilterController.cs:112-252).
// It returns the genre entities and the audio and subtitle stream languages
// of the scoped items. Tags stays empty, as in pinned upstream; Jellyfin 12.1
// fills it (D-128).
//
// The scope reads only the parent's subtree. A recursive scope under a
// folder is a recursive ParentId query, which ItemStore drives from an index
// keyed by the parent; see applyRecursiveParentScope.
func (h itemNamesHandler) queryFilters(w http.ResponseWriter, r *http.Request) {
	if h.reader == nil || h.facts == nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	scope, _, ok := h.itemNamesQuery(w, r)
	if !ok {
		return
	}
	var request queryFiltersRequest
	if err := bind.QueryRequest(r, &request); err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	kinds := scope.IncludeItemTypes
	query := store.ItemQuery{
		User:             scope.User,
		UserID:           scope.UserID,
		IncludeItemTypes: kinds,
		IsAiring:         request.IsAiring,
		IsMovie:          request.IsMovie,
		IsSports:         request.IsSports,
		IsKids:           request.IsKids,
		IsNews:           request.IsNews,
		IsSeries:         request.IsSeries,
	}

	// A single Trailer or Program kind ignores the parent. A parent that
	// does not exist leaves the query unscoped, as GetItemById returns null.
	var parent *item.Item
	if scope.ParentID != nil && !(len(kinds) == 1 && (kinds[0] == item.KindTrailer || kinds[0] == item.KindProgram)) {
		value, err := h.parent(r.Context(), *scope.ParentID, scope.UserID)
		if err != nil {
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}
		parent = value
	}
	if parent != nil {
		parentID := parent.ID
		recursive := request.Recursive == nil || *request.Recursive
		switch {
		case !recursive && parent.Kind != item.KindUserView && parent.Kind != item.KindCollectionFolder:
			// InternalItemsQuery.Parent: the direct children only.
			query.ParentID = &parentID
		case parent.Kind == item.KindBoxSet || parent.Kind == item.KindPlaylist:
			// A recursive ParentId of these kinds reads linked children,
			// but AncestorIds reads stored descendants.
			query.AncestorIDs = []dto.UUID{parentID}
		default:
			// AncestorIds = [parent]. The recursive ParentId scope selects
			// the same rows through the parent's subtree.
			query.ParentID = &parentID
			query.Recursive = true
		}
	}

	filters := dto.NewQueryFilters()
	genreKind := item.KindGenre
	if len(kinds) == 1 && (kinds[0] == item.KindMusicAlbum || kinds[0] == item.KindMusicVideo || kinds[0] == item.KindMusicArtist || kinds[0] == item.KindAudio) {
		genreKind = item.KindMusicGenre
	}
	genres, err := h.namedValueNames(r.Context(), h.facts, query, genreKind, nil)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	for _, genre := range genres {
		name := ""
		if genre.Name != nil {
			name = *genre.Name
		}
		filters.Genres = append(filters.Genres, dto.NameGuidPair{Name: name, ID: genre.ID})
	}

	languages, ok := h.reader.(ItemStreamLanguageReader)
	if ok && slices.ContainsFunc(kinds, func(kind item.Kind) bool {
		return kind == item.KindMovie || kind == item.KindSeries || kind == item.KindSeason || kind == item.KindEpisode
	}) {
		// Alternate versions can carry other languages, so owned items are
		// included. Streams belong to episodes, not to series or seasons.
		streams := query
		streams.IncludeOwnedItems = true
		if (slices.Contains(kinds, item.KindSeries) || slices.Contains(kinds, item.KindSeason)) && !slices.Contains(kinds, item.KindEpisode) {
			streams.IncludeItemTypes = append(slices.Clone(kinds), item.KindEpisode)
		}
		if filters.AudioLanguages, err = h.streamLanguages(r.Context(), languages, streams, "Audio"); err != nil {
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}
		if filters.SubtitleLanguages, err = h.streamLanguages(r.Context(), languages, streams, "Subtitle"); err != nil {
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}
	}
	_ = render.JSON(w, r, http.StatusOK, filters)
}

// streamLanguages names each language "DisplayName (language)" when a
// culture matches and orders the pairs by name.
func (h itemNamesHandler) streamLanguages(ctx context.Context, reader ItemStreamLanguageReader, query store.ItemQuery, streamType string) ([]dto.NameValuePair, error) {
	values, err := reader.QueryStreamLanguages(ctx, query, streamType)
	if err != nil {
		return nil, err
	}
	out := make([]dto.NameValuePair, 0, len(values))
	for _, language := range values {
		name := language
		if h.languages != nil {
			if culture, found := h.languages.FindLanguageInfo(language); found {
				name = culture.DisplayName + " (" + language + ")"
			}
		}
		out = append(out, dto.NameValuePair{Name: name, Value: language})
	}
	sort.SliceStable(out, func(i, j int) bool {
		left, right := strings.ToLower(out[i].Name), strings.ToLower(out[j].Name)
		if left != right {
			return left < right
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

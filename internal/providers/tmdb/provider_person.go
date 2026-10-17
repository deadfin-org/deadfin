// SPDX-License-Identifier: GPL-2.0-only

package tmdb

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"

	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/providers"
)

// RefreshPerson ports TmdbPersonProvider.GetMetadata and
// TmdbPersonImageProvider.GetImages. The metadata step resolves the TMDb
// person id from the provider ids or, without one, from the first
// /search/person result for the name. The image step uses only a stored or
// resolved TMDb id. Both steps read one /person/{id} response, as upstream
// reads one cached GetPersonAsync result.
func (provider *Provider) RefreshPerson(ctx context.Context, request providers.PersonRefresh, target *item.Item) (bool, error) {
	if target == nil {
		return false, errors.New("tmdb: person is required")
	}
	if provider.api == nil || target.Kind != item.KindPerson {
		return false, nil
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	metadata := request.Metadata && !target.IsLocked
	if !metadata && !request.Images {
		return false, nil
	}
	language := normalizeLanguage(strings.TrimSpace(request.Language), request.Country)

	tmdbID := personTMDbID(*target)
	if tmdbID <= 0 && metadata && target.Name != nil && strings.TrimSpace(*target.Name) != "" {
		results, err := provider.api.searchPerson(ctx, *target.Name, provider.config.IncludeAdult)
		if err != nil {
			return false, err
		}
		if len(results) > 0 {
			tmdbID = results[0].ID
		}
	}
	if tmdbID <= 0 {
		return false, nil
	}
	result, err := provider.api.getPerson(ctx, tmdbID, language)
	if err != nil {
		return false, err
	}
	if result == nil {
		return false, nil
	}
	if metadata {
		applyPerson(target, result, request.ReplaceAllMetadata)
	}
	if request.Images {
		provider.applyPersonImages(ctx, target, result)
	}
	return true, nil
}

func personTMDbID(target item.Item) int {
	value, ok := target.ProviderIDs.Get("Tmdb")
	if !ok {
		return 0
	}
	id, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return 0
	}
	return id
}

// applyPerson maps a TMDb person onto target. GetMetadata keeps the incoming
// name and sets Overview, PremiereDate, EndDate, ProductionLocations, and the
// Tmdb and Imdb ids. MergeBaseItemData then fills only empty fields unless
// replace is set, keeps locked fields, adds new production locations, and
// adds provider ids without replacing a stored one.
func applyPerson(target *item.Item, result *person, replace bool) {
	if !personFieldLocked(*target, item.MetadataFieldOverview) && (replace || target.Overview == nil || *target.Overview == "") {
		setString(&target.Overview, result.Biography)
	}
	if birthday, ok := parseTMDbDate(result.Birthday); ok && (replace || target.PremiereDate == nil) {
		target.PremiereDate = &birthday
	}
	if deathday, ok := parseTMDbDate(result.Deathday); ok && (replace || target.EndDate == nil) {
		target.EndDate = &deathday
	}
	if place := strings.TrimSpace(result.PlaceOfBirth); place != "" && !personFieldLocked(*target, item.MetadataFieldProductionLocations) {
		locations := target.Values.ProductionLocations
		switch {
		case replace || len(locations) == 0:
			target.Values.ProductionLocations = []string{result.PlaceOfBirth}
		case !slices.ContainsFunc(locations, func(value string) bool { return strings.EqualFold(value, result.PlaceOfBirth) }):
			target.Values.ProductionLocations = append(slices.Clone(locations), result.PlaceOfBirth)
		}
	}
	setPersonProviderID(target, "Tmdb", strconv.Itoa(result.ID), replace)
	setPersonProviderID(target, "Imdb", strings.TrimSpace(result.ImdbID), replace)
}

func setPersonProviderID(target *item.Item, name, value string, replace bool) {
	if value == "" {
		return
	}
	if _, found := target.ProviderIDs.Get(name); found && !replace {
		return
	}
	target.ProviderIDs.Set(name, value)
}

func personFieldLocked(target item.Item, field item.MetadataField) bool {
	return slices.Contains(target.LockedFields, field)
}

// applyPersonImages ports TmdbPersonImageProvider: the profile images become
// Primary candidates. The first profile fills a missing Primary image.
func (provider *Provider) applyPersonImages(ctx context.Context, target *item.Item, result *person) {
	if result.Images == nil || len(result.Images.Profiles) == 0 || result.Images.Profiles[0].FilePath == "" {
		return
	}
	primary := result.Images.Profiles[0]
	url := imageURL(provider.api.imageBaseURL(ctx), provider.config.imageSizeOrOriginal(), primary.FilePath)
	putRemoteImage(target, item.ImageTypePrimary, url, primary.Width, primary.Height, false)
}

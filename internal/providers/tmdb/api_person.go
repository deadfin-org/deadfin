// SPDX-License-Identifier: GPL-2.0-only

package tmdb

import (
	"context"
	"log/slog"
	"net/url"
	"strconv"
)

// person is the /person/{id} response with the appended sub-resources the
// ported person providers read (images).
type person struct {
	ID           int    `json:"id"`
	Name         string `json:"name"`
	Biography    string `json:"biography"`
	Birthday     string `json:"birthday"`
	Deathday     string `json:"deathday"`
	PlaceOfBirth string `json:"place_of_birth"`
	Homepage     string `json:"homepage"`
	ImdbID       string `json:"imdb_id"`
	Images       *struct {
		Profiles []imageData `json:"profiles"`
	} `json:"images"`
}

type searchPerson struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	ProfilePath string `json:"profile_path"`
}

type searchPersonResponse struct {
	Results []searchPerson `json:"results"`
}

// getPerson ports TmdbClientManager.GetPersonAsync. Upstream also appends
// tv_credits, movie_credits, and external_ids. The person providers read only
// the top-level fields and the images, so this request leaves those out.
func (client *apiClient) getPerson(ctx context.Context, tmdbID int, language string) (*person, error) {
	query := url.Values{}
	if language != "" {
		query.Set("language", language)
	}
	query.Set("append_to_response", "images")
	var result person
	found, err := client.getJSON(ctx, "/person/"+strconv.Itoa(tmdbID), query, &result)
	if err != nil {
		client.logger.LogAttrs(ctx, slog.LevelWarn, "tmdb get failed", slog.String("op", "person"), slog.Int("tmdbId", tmdbID), slog.String("error", "request failed"))
		return nil, err
	}
	client.logger.LogAttrs(ctx, slog.LevelDebug, "tmdb get", slog.String("op", "person"), slog.Int("tmdbId", tmdbID), slog.Bool("found", found))
	if !found {
		return nil, nil
	}
	return &result, nil
}

// searchPerson ports TmdbClientManager.SearchPersonAsync. Upstream sends no
// language.
func (client *apiClient) searchPerson(ctx context.Context, name string, includeAdult bool) ([]searchPerson, error) {
	query := url.Values{}
	query.Set("query", name)
	query.Set("include_adult", strconv.FormatBool(includeAdult))
	var response searchPersonResponse
	if _, err := client.getJSON(ctx, "/search/person", query, &response); err != nil {
		client.logger.LogAttrs(ctx, slog.LevelWarn, "tmdb search failed", slog.String("op", "search/person"), slog.String("error", "request failed"))
		return nil, err
	}
	client.logger.LogAttrs(ctx, slog.LevelDebug, "tmdb search", slog.String("op", "search/person"), slog.Int("results", len(response.Results)))
	return response.Results, nil
}

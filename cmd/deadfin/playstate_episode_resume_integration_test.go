// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/playback"
	"github.com/deadfin-org/deadfin/internal/store"
)

// TestApplicationPlaystatePersistsEpisodeResume replays the report sequence
// Swiftfin's native player sends for a direct-played episode. The series has
// a Tvdb ID, so upstream's first user-data key for the episode is the series
// key plus season and episode numbers ("280619004007"). The write path uses
// that key, and the item DTO, the Resume list, and PlaybackInfo must read it
// back (UserDataManager.GetUserData matches item.GetUserDataKeys in order).
func TestApplicationPlaystatePersistsEpisodeResume(t *testing.T) {
	const (
		runtimeTicks  int64 = 2663 * 10_000_000
		progressTicks int64 = 300 * 10_000_000
		stopTicks     int64 = 600 * 10_000_000
	)

	app := openPlaystateTracerApplication(t, filepath.Join(t.TempDir(), "program-data"))
	t.Cleanup(func() {
		if err := app.Close(context.Background()); err != nil {
			t.Errorf("close episode playstate application: %v", err)
		}
	})
	configureComposedAdministrator(t, app.Router())
	token, userID := loginPlaystateTracerAdministrator(t, app)
	episodeID := seedPlaystateEpisode(t, app, runtimeTicks)
	playSessionID := mintPlaystateTracerSession(t, app, token, episodeID)

	id := episodeID.String()
	for _, report := range []struct {
		name, path, body string
	}{
		{"start", "/Sessions/Playing", fmt.Sprintf(`{"ItemId":%q,"MediaSourceId":%q,"PlaySessionId":%q,"PositionTicks":0,"CanSeek":true,"IsPaused":false,"IsMuted":false,"PlayMethod":"DirectPlay","AudioStreamIndex":1}`, id, id, playSessionID)},
		{"progress", "/Sessions/Playing/Progress", fmt.Sprintf(`{"ItemId":%q,"MediaSourceId":%q,"PlaySessionId":%q,"PositionTicks":%d,"CanSeek":true,"IsPaused":false,"IsMuted":false,"PlayMethod":"DirectPlay","AudioStreamIndex":1}`, id, id, playSessionID, progressTicks)},
	} {
		record := authenticatedCompositionRequest(app.Router(), http.MethodPost, report.path, []byte(report.body), token)
		if record.Code != http.StatusNoContent {
			t.Fatalf("%s report status = %d, want %d; body: %s", report.name, record.Code, http.StatusNoContent, record.Body.String())
		}
	}

	itemPath := fmt.Sprintf("/Users/%s/Items/%s", userID, episodeID)
	data := playstateEpisodeUserData(t, app, token, itemPath)
	if data.PlaybackPositionTicks != progressTicks || data.LastPlayedDate == nil || data.Key != "280619004007" {
		t.Fatalf("UserData after progress = %+v, want PlaybackPositionTicks=%d, LastPlayedDate set, Key=280619004007", data, progressTicks)
	}

	stop := fmt.Sprintf(`{"ItemId":%q,"MediaSourceId":%q,"PlaySessionId":%q,"PositionTicks":%d}`, id, id, playSessionID, stopTicks)
	if record := authenticatedCompositionRequest(app.Router(), http.MethodPost, "/Sessions/Playing/Stopped", []byte(stop), token); record.Code != http.StatusNoContent {
		t.Fatalf("stop report status = %d, want %d; body: %s", record.Code, http.StatusNoContent, record.Body.String())
	}
	data = playstateEpisodeUserData(t, app, token, itemPath)
	if data.PlaybackPositionTicks != stopTicks || data.Played || data.PlayCount != 1 || data.LastPlayedDate == nil {
		t.Fatalf("UserData after stop = %+v, want PlaybackPositionTicks=%d, Played=false, PlayCount=1, LastPlayedDate set", data, stopTicks)
	}

	resume := authenticatedCompositionRequest(app.Router(), http.MethodGet, "/UserItems/Resume?userId="+userID.String(), nil, token)
	if resume.Code != http.StatusOK {
		t.Fatalf("GET Resume status = %d; body: %s", resume.Code, resume.Body.String())
	}
	var page struct {
		Items []struct {
			ID       string                      `json:"Id"`
			UserData playstateEpisodeUserDataDTO `json:"UserData"`
		} `json:"Items"`
	}
	if err := json.Unmarshal(resume.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode Resume: %v; body: %s", err, resume.Body.String())
	}
	if len(page.Items) != 1 || page.Items[0].ID != id || page.Items[0].UserData.PlaybackPositionTicks != stopTicks {
		t.Fatalf("Resume = %s, want the episode at %d ticks", resume.Body.String(), stopTicks)
	}

	versions, err := store.NewItemStore(app.stores).VersionUserData(t.Context(), userID, []dto.UUID{episodeID})
	if err != nil {
		t.Fatalf("VersionUserData: %v", err)
	}
	if got, found := versions[episodeID]; !found || got.PositionTicks != stopTicks {
		t.Fatalf("VersionUserData = %+v (found %t), want PositionTicks=%d", got, found, stopTicks)
	}
}

type playstateEpisodeUserDataDTO struct {
	PlaybackPositionTicks int64      `json:"PlaybackPositionTicks"`
	PlayCount             int        `json:"PlayCount"`
	Played                bool       `json:"Played"`
	LastPlayedDate        *time.Time `json:"LastPlayedDate"`
	Key                   string     `json:"Key"`
}

func playstateEpisodeUserData(t *testing.T, app *application, token, path string) playstateEpisodeUserDataDTO {
	t.Helper()

	record := authenticatedCompositionRequest(app.Router(), http.MethodGet, path, nil, token)
	if record.Code != http.StatusOK {
		t.Fatalf("GET %s status = %d; body: %s", path, record.Code, record.Body.String())
	}
	var body struct {
		UserData playstateEpisodeUserDataDTO `json:"UserData"`
	}
	if err := json.Unmarshal(record.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %s: %v; body: %s", path, err, record.Body.String())
	}
	return body.UserData
}

func seedPlaystateEpisode(t *testing.T, app *application, runtimeTicks int64) dto.UUID {
	t.Helper()

	parse := func(value string) dto.UUID {
		id, err := dto.ParseUUID(value)
		if err != nil {
			t.Fatalf("parse %q: %v", value, err)
		}
		return id
	}
	seriesID, seasonID, episodeID := parse("00000000000000000000000000b50001"), parse("00000000000000000000000000b50002"), parse("00000000000000000000000000b50003")
	seriesName, seasonName, episodeName := "The Expanse", "Season 4", "A Shot in the Dark"
	seriesPath, seasonPath, episodePath := "/library/tv/The Expanse", "/library/tv/The Expanse/Season 4", "/library/tv/The Expanse/Season 4/S04E07.mkv"
	container := "mkv"
	size, bitrate := int64(1_000_000), 8_500_000
	season, episode := 4, 7
	var providers item.ProviderIDs
	providers.Set("Tvdb", "280619")
	providers.Set("Imdb", "tt3230854")

	items := store.NewItemStore(app.stores)
	for _, value := range []item.Item{
		{ID: seriesID, Kind: item.KindSeries, IsFolder: true, Name: &seriesName, Path: &seriesPath, ProviderIDs: providers},
		{ID: seasonID, Kind: item.KindSeason, IsFolder: true, ParentID: &seriesID, SeriesID: &seriesID, Name: &seasonName, Path: &seasonPath, IndexNumber: &season},
		{ID: episodeID, Kind: item.KindEpisode, ParentID: &seasonID, SeriesID: &seriesID, SeasonID: &seasonID, Name: &episodeName, Path: &episodePath, Container: &container, Size: &size, RunTimeTicks: &runtimeTicks, TotalBitrate: &bitrate, ParentIndexNumber: &season, IndexNumber: &episode},
	} {
		if err := items.Upsert(t.Context(), value); err != nil {
			t.Fatalf("upsert %s: %v", value.Kind, err)
		}
	}
	if err := items.ReplaceStreams(t.Context(), episodeID, []playback.MediaStream{{Index: 0, Type: playback.StreamTypeVideo, Codec: "hevc"}, {Index: 1, Type: playback.StreamTypeAudio, Codec: "ac3"}}); err != nil {
		t.Fatalf("store episode streams: %v", err)
	}
	return episodeID
}

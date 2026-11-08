// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/api/handlers"
	"github.com/deadfin-org/deadfin/internal/api/middleware"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/dtobuild"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
	"github.com/deadfin-org/deadfin/internal/store/storetest"
)

func TestLiveTvRecommendedRankingSQLite(t *testing.T) {
	stores, err := sqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stores.Close() })
	runLiveTvRecommendedRanking(t, stores)
}

func TestLiveTvRecommendedRankingPostgres(t *testing.T) {
	runLiveTvRecommendedRanking(t, storetest.OpenPostgres(t))
}

// recommendedProgramme is one channel of the ranking guide with its one
// programme.
type recommendedProgramme struct {
	name  string
	start time.Time
	stop  time.Time
	extra string
}

// runLiveTvRecommendedRanking pins LiveTvManager.GetRecommendedProgramsInternal
// (LiveTvManager.cs:272-322) and GetRecommendationScore (LiveTvManager.cs:
// 341-381): the query reads the first Max(limit * 4, 200) airing programmes
// in start order; the result orders them by the UTC day of the start, then
// by the score, descending, with ties in start order; then it takes limit.
func runLiveTvRecommendedRanking(t *testing.T, stores store.Stores) {
	t.Helper()
	ctx := t.Context()
	now := time.Date(2026, 3, 8, 12, 0, 0, 0, time.UTC)
	today := func(hour, minute int) time.Time { return time.Date(2026, 3, 8, hour, minute, 0, 0, time.UTC) }
	series := `<category>Series</category><episode-num system="xmltv_ns">0.1.</episode-num>`
	programmes := []recommendedProgramme{
		// Started yesterday: its day sorts before every score of today.
		{name: "yesterday", start: time.Date(2026, 3, 7, 23, 0, 0, 0, time.UTC), stop: today(14, 0)},
		{name: "favorite", start: today(11, 0), stop: today(13, 0)},
		{name: "live", start: today(11, 30), stop: today(12, 30), extra: "<live/>"},
		{name: "new episode", start: today(11, 45), stop: today(12, 15), extra: series},
		{name: "repeat", start: today(11, 15), stop: today(12, 15), extra: series + "<previously-shown/>"},
		{name: "disliked", start: today(10, 0), stop: today(13, 0)},
		{name: "played", start: today(11, 50), stop: today(12, 50)},
		{name: "ended", start: today(10, 0), stop: today(11, 0)},
		{name: "upcoming", start: today(12, 30), stop: today(13, 0)},
	}
	// Fillers start before every ranked programme of today. With enough
	// of them, they fill the Max(limit * 4, 200) programmes that the query
	// reads, and the ranked programmes are not candidates.
	for index := range 200 {
		programmes = append(programmes, recommendedProgramme{name: fmt.Sprintf("filler %03d", index), start: today(9, 0), stop: today(13, 0)})
	}

	dir := t.TempDir()
	var playlist, xmltv strings.Builder
	playlist.WriteString("#EXTM3U\n")
	xmltv.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n<tv>\n")
	for index, programme := range programmes {
		id := fmt.Sprintf("ch%03d", index)
		fmt.Fprintf(&playlist, "#EXTINF:-1 tvg-id=%q tvg-chno=\"%d\",%s\nhttp://127.0.0.1:1/%s\n", id, index+1, programme.name, id)
		fmt.Fprintf(&xmltv, " <channel id=%q><display-name>%s</display-name></channel>\n", id, programme.name)
		fmt.Fprintf(&xmltv, " <programme start=%q stop=%q channel=%q><title>%s</title>%s</programme>\n",
			programme.start.Format("20060102150405 -0700"), programme.stop.Format("20060102150405 -0700"), id, programme.name, programme.extra)
	}
	xmltv.WriteString("</tv>\n")
	playlistPath, xmltvPath := filepath.Join(dir, "ranking.m3u"), filepath.Join(dir, "ranking.xml")
	if err := os.WriteFile(playlistPath, []byte(playlist.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(xmltvPath, []byte(xmltv.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	items := store.NewItemStore(stores)
	clk := clock.NewFake(now)
	days := 1
	service := liveTvGuideFrom(t, items, clk, playlistPath, xmltvPath, &days)

	admin := store.User{ID: itemsHandlerUUID(t, 910), Policy: []byte(`{"IsAdministrator":true,"EnableAllFolders":true,"EnableLiveTvAccess":true}`)}
	users := itemsAccessUsers{values: map[dto.UUID]store.User{admin.ID: admin}}
	authorizer := middleware.NewAuthorizer(itemsAccessResolver{identities: map[string]auth.Identity{
		admin.ID.String(): {Authenticated: true, UserID: admin.ID, Administrator: true},
	}}, itemsAccessPolicy{}, users)
	builder := dtobuild.NewBuilder(items, dtobuild.NewStoreEnricher(items, users, nil)).WithClock(clk)
	rt := router.New(authorizer.Wrap)
	handlers.RegisterLiveTv(rt, &fakeLiveTvAdmin{}, handlers.LiveTvReads{Guide: service, Items: items, Build: builder, Users: users, Clock: clk, AllUsers: liveTvReadUsers{users}})

	// The user's channel data: favorite +3, a like +2 or a dislike -2, and
	// each play +1.
	channels, err := items.Query(ctx, store.ItemQuery{IncludeItemTypes: []item.Kind{item.KindLiveTvChannel}})
	if err != nil {
		t.Fatal(err)
	}
	dislike, played := 0, 0
	for _, channel := range channels.Items {
		data := store.UserData{UserID: admin.ID, CustomKey: item.UserDataKeys(channel, item.UserDataInputs{})[0], ItemID: channel.ID}
		switch *channel.Name {
		case "favorite":
			data.IsFavorite = true
		case "disliked":
			data.Likes, data.PlayCount = &dislike, 1
		case "played":
			played = 2
			data.PlayCount = played
		default:
			continue
		}
		if err := items.UpsertUserData(ctx, data); err != nil {
			t.Fatal(err)
		}
	}

	recommended := func(t *testing.T, limit int) dto.QueryResult[dto.BaseItemDto] {
		t.Helper()
		record := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/LiveTv/Programs/Recommended?isAiring=true&limit=%d", limit), nil)
		request.Header.Set("X-Item-Access-Actor", admin.ID.String())
		rt.ServeHTTP(record, request)
		if record.Code != http.StatusOK {
			t.Fatalf("status %d: %s", record.Code, record.Body.String())
		}
		var page dto.QueryResult[dto.BaseItemDto]
		if err := json.Unmarshal(record.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		return page
	}
	names := func(page dto.QueryResult[dto.BaseItemDto]) []string {
		out := make([]string, 0, len(page.Items))
		for _, value := range page.Items {
			out = append(out, value.Name)
		}
		return out
	}

	t.Run("candidates are the first airing starts", func(t *testing.T) {
		// limit 12 reads Max(48, 200) = 200 programmes: yesterday, the
		// disliked programme (10:00) and 198 fillers (09:00).
		page := recommended(t, 12)
		want := []string{"yesterday", "filler 000", "filler 001", "filler 002", "filler 003", "filler 004",
			"filler 005", "filler 006", "filler 007", "filler 008", "filler 009", "filler 010"}
		if got := names(page); !reflect.DeepEqual(got, want) {
			t.Fatalf("names %q, want %q", got, want)
		}
		if page.TotalRecordCount != 200 {
			t.Fatalf("total %d, want the 200 candidates", page.TotalRecordCount)
		}
	})

	t.Run("day then score", func(t *testing.T) {
		// limit 60 reads all 207 airing programmes.
		page := recommended(t, 60)
		want := []string{"yesterday", "favorite", "played", "live", "new episode"}
		if got := names(page)[:len(want)]; !reflect.DeepEqual(got, want) {
			t.Fatalf("first names %q, want %q", got, want)
		}
		// Score 0: the fillers and the repeat, in start order; the
		// disliked channel (-2 + 1 play) is last.
		got := names(page)
		if got[5] != "filler 000" || got[len(got)-1] != "filler 054" {
			t.Fatalf("names %q", got)
		}
		all := recommended(t, 300)
		gotAll := names(all)
		if all.TotalRecordCount != 207 || gotAll[len(gotAll)-2] != "repeat" || gotAll[len(gotAll)-1] != "disliked" {
			t.Fatalf("total %d tail %q", all.TotalRecordCount, gotAll[len(gotAll)-3:])
		}
	})
}

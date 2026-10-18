// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/api/handlers"
	"github.com/deadfin-org/deadfin/internal/api/middleware"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/dtobuild"
	"github.com/deadfin-org/deadfin/internal/session"
	"github.com/deadfin-org/deadfin/internal/store"
)

type resumeSessions []session.Snapshot

func (sessions resumeSessions) Sessions(session.Query) []session.Snapshot { return sessions }

// TestResumeRoutesScopeToParent pins ItemsController.GetResumeItems
// (ItemsController.cs:947-997) through the real ItemStore: parentId keeps the
// in-progress items under the parent, for a series and for a library. The
// LatestItemExcludes preference narrows only the unscoped list, and
// excludeActiveSessions drops the items the user's sessions are playing.
func TestResumeRoutesScopeToParent(t *testing.T) {
	fixture := newLatestItemsExcludesFixture(t)
	includedLibrary := latestItemsExcludesUUID(t, 1)
	includedSeries := latestItemsExcludesUUID(t, 30)
	includedEpisode := latestItemsExcludesUUID(t, 32)
	excludedMovie := latestItemsExcludesUUID(t, 21)
	// The episode is the most recent playback, so it leads the unscoped list.
	played := time.Date(2026, time.July, 27, 0, 0, 0, 0, time.UTC)
	if err := fixture.items.UpsertUserData(t.Context(), store.UserData{UserID: fixture.target.ID, CustomKey: "included-next", ItemID: includedEpisode, PositionTicks: 1, LastPlayed: &played}); err != nil {
		t.Fatalf("upsert episode user data: %v", err)
	}

	authorizer := middleware.NewAuthorizer(
		latestItemsExcludesResolver{identity: auth.Identity{Authenticated: true, Administrator: true, UserID: fixture.admin.ID}},
		latestItemsExcludesEvaluator{},
		fixture.users,
	)
	routes := router.New(authorizer.Wrap)
	sessions := resumeSessions{
		{UserID: fixture.admin.ID, NowPlaying: &session.PlaybackState{ItemID: includedEpisode}},
		{UserID: fixture.target.ID, NowPlaying: &session.PlaybackState{ItemID: fixture.includedMovie.ID}},
		{UserID: fixture.target.ID},
	}
	handlers.RegisterItems(routes, fixture.items, dtobuild.NewBuilder(fixture.items, nil), clock.NewReal(),
		handlers.ItemServices{Users: fixture.users, LatestPreferences: handlers.NewLatestUserPreferences(fixture.users)}, sessions)

	target := fixture.target.ID.Dashed()
	for _, test := range []struct {
		name string
		path string
		want []dto.UUID
	}{
		{name: "unscoped", path: "/UserItems/Resume?userId=" + target, want: []dto.UUID{includedEpisode, fixture.includedMovie.ID}},
		{name: "series", path: "/UserItems/Resume?userId=" + target + "&parentId=" + includedSeries.String(), want: []dto.UUID{includedEpisode}},
		{name: "legacy series", path: "/Users/" + target + "/Items/Resume?ParentId=" + includedSeries.Dashed(), want: []dto.UUID{includedEpisode}},
		{name: "library", path: "/UserItems/Resume?userId=" + target + "&parentId=" + includedLibrary.String() + "&includeItemTypes=Movie", want: []dto.UUID{fixture.includedMovie.ID}},
		{name: "library without movies", path: "/UserItems/Resume?userId=" + target + "&parentId=" + includedLibrary.String() + "&excludeItemTypes=Movie", want: []dto.UUID{includedEpisode}},
		{name: "excluded library when scoped", path: "/UserItems/Resume?userId=" + target + "&parentId=" + fixture.excludedLibrary.ID.String(), want: []dto.UUID{excludedMovie}},
		{name: "audio only", path: "/UserItems/Resume?userId=" + target + "&mediaTypes=Audio", want: []dto.UUID{}},
		{name: "exclude active sessions", path: "/UserItems/Resume?userId=" + target + "&excludeActiveSessions=true", want: []dto.UUID{includedEpisode}},
	} {
		t.Run(test.name, func(t *testing.T) {
			record := httptest.NewRecorder()
			routes.ServeHTTP(record, httptest.NewRequest(http.MethodGet, test.path, nil))
			if record.Code != http.StatusOK {
				t.Fatalf("GET %s status = %d, want 200; body=%s", test.path, record.Code, record.Body.String())
			}
			got := latestItemsExcludesResponseIDs(t, record.Body.Bytes())
			if got == nil {
				got = []dto.UUID{}
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("GET %s item IDs = %v, want %v; body=%s", test.path, got, test.want, record.Body.String())
			}
		})
	}
}

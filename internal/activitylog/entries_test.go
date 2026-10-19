// SPDX-License-Identifier: GPL-2.0-only

package activitylog_test

import (
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/activitylog"
	"github.com/deadfin-org/deadfin/internal/dto"
)

// TestEntryBuildersPortTheUpstreamConsumers pins the names, types, overviews
// and severities of the event consumers under
// Jellyfin.Server.Implementations/Events/Consumers with the en-US strings.
func TestEntryBuildersPortTheUpstreamConsumers(t *testing.T) {
	user := dto.UUID{0xaa}
	item := activitylog.PlaybackItem{ID: dto.UUID{0xbb}, Name: "Pilot", SeriesName: "Show", MediaType: "Video"}
	song := activitylog.PlaybackItem{ID: dto.UUID{0xcc}, Name: "Song", Artists: []string{"Band", "Other"}, MediaType: "Audio"}
	book := activitylog.PlaybackItem{ID: dto.UUID{0xdd}, Name: "Book", MediaType: "Book"}
	stopVideo, okVideo := activitylog.PlaybackStopped(user, "alice", item, "TV")
	_, okBook := activitylog.PlaybackStopped(user, "alice", book, "TV")
	if !okVideo || okBook {
		t.Fatalf("PlaybackStopped logged = video %v book %v; want true false", okVideo, okBook)
	}

	for _, tc := range []struct {
		entry                   activitylog.Entry
		name, kind, severity    string
		short, overview, itemID string
		user                    dto.UUID
	}{
		{activitylog.AuthenticationSucceeded(user, "alice", "10.0.0.2"), "alice successfully authenticated", "AuthenticationSucceeded", "Information", "IP address: 10.0.0.2", "", "", user},
		{activitylog.AuthenticationFailed("mallory", "10.0.0.3"), "Failed login attempt from mallory", "AuthenticationFailed", "Error", "IP address: 10.0.0.3", "", "", dto.UUID{}},
		{activitylog.SessionStarted(user, "alice", "TV", "10.0.0.2"), "alice is online from TV", "SessionStarted", "Information", "IP address: 10.0.0.2", "", "", user},
		{activitylog.SessionEnded(user, "alice", "TV", "10.0.0.2"), "alice has disconnected from TV", "SessionEnded", "Information", "IP address: 10.0.0.2", "", "", user},
		{activitylog.PlaybackStarted(user, "alice", item, "TV"), "alice is playing Show - Pilot on TV", "VideoPlayback", "Information", "", "", item.ID.String(), user},
		{activitylog.PlaybackStarted(user, "alice", song, "TV"), "alice is playing Band - Song on TV", "AudioPlayback", "Information", "", "", song.ID.String(), user},
		{activitylog.PlaybackStarted(user, "alice", book, "TV"), "alice is playing Book on TV", "Playback", "Information", "", "", book.ID.String(), user},
		{stopVideo, "alice has finished playing Show - Pilot on TV", "VideoPlaybackStopped", "Information", "", "", item.ID.String(), user},
		{activitylog.UserCreated(user, "alice"), "User alice has been created", "UserCreated", "Information", "", "", "", user},
		{activitylog.UserDeleted("alice"), "User alice has been deleted", "UserDeleted", "Information", "", "", "", dto.UUID{}},
		{activitylog.UserPasswordChanged(user, "alice"), "Password has been changed for user alice", "UserPasswordChanged", "Information", "", "", "", user},
		{activitylog.UserLockedOut(user, "alice"), "User alice has been locked out", "UserLockedOut", "Error", "", "", "", user},
		{activitylog.TaskFailed("Scan Media Library", "boom", "trace", 90*time.Minute+5*time.Second), "Scan Media Library failed", "TaskFailed", "Error", "Running time: 1 hour, 30 minutes and 5 seconds", "boom\ntrace", "", dto.UUID{}},
	} {
		e := tc.entry
		if e.Name != tc.name || e.Type != tc.kind || e.Severity != tc.severity || e.UserID != tc.user ||
			deref(e.ShortOverview) != tc.short || deref(e.Overview) != tc.overview || deref(e.ItemID) != tc.itemID {
			t.Errorf("entry = %+v (short %q overview %q item %q); want %q %q %q short %q overview %q item %q",
				e, deref(e.ShortOverview), deref(e.Overview), deref(e.ItemID), tc.name, tc.kind, tc.severity, tc.short, tc.overview, tc.itemID)
		}
	}
}

// TestTaskFailedRunningTime ports TaskCompletedLogger.ToUserFriendlyString.
func TestTaskFailedRunningTime(t *testing.T) {
	day := 24 * time.Hour
	for _, tc := range []struct {
		span time.Duration
		want string
	}{
		{0, "Running time: 0 seconds"},
		{time.Second, "Running time: 1 second"},
		{2 * time.Minute, "Running time: 2 minutes"},
		{time.Hour + time.Second, "Running time: 1 hour and 1 second"},
		{400*day + 3*time.Hour, "Running time: 1 year, 1 month, 5 days and 3 hours"},
		{250 * 365 * day, "Running time: 250 years"},
	} {
		if got := deref(activitylog.TaskFailed("t", "", "", tc.span).ShortOverview); got != tc.want {
			t.Errorf("span %v = %q; want %q", tc.span, got, tc.want)
		}
	}
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

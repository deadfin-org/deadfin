// SPDX-License-Identifier: GPL-2.0-only

package playstate_test

import (
	"testing"

	"github.com/deadfin-org/deadfin/internal/activitylog"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/playstate"
	"github.com/deadfin-org/deadfin/internal/session"
	"github.com/deadfin-org/deadfin/internal/store"
)

type recordedActivity struct{ entries []activitylog.Entry }

func (r *recordedActivity) Record(entry activitylog.Entry) { r.entries = append(r.entries, entry) }

// TestServiceRecordsPlaybackActivity defends the playback activity entries
// (PlaybackStartLogger, PlaybackStopLogger): the first session user and the
// session device name the entry, a failed stop still writes the stop entry,
// an audio item names its first artist, and theme media and unknown items
// write nothing.
func TestServiceRecordsPlaybackActivity(t *testing.T) {
	fixture := newPlaystateServiceFixture(t, item.KindMovie)
	users := playstateServiceUsers{byID: map[dto.UUID]store.User{
		fixture.primaryUser:    {ID: fixture.primaryUser, Name: "alice", Configuration: []byte(`{}`)},
		fixture.additionalUser: {ID: fixture.additionalUser, Name: "bob", Configuration: []byte(`{}`)},
	}}
	activity := &recordedActivity{}
	service := playstate.NewService(fixture.clock, playstateServiceConfiguration{}, fixture.items, users, fixture.sessions, fixture.changes).WithActivity(activity)
	ctx := t.Context()

	songName, themeName := "Song", "Theme"
	theme := item.ExtraTypeThemeVideo
	song := item.Item{ID: dto.UUID{0x51}, Kind: item.KindAudio, Name: &songName, Values: item.ItemValues{Artists: []string{"Band", "Other"}}}
	themeVideo := item.Item{ID: dto.UUID{0x52}, Kind: item.KindVideo, Name: &themeName, ExtraType: &theme}
	for _, value := range []item.Item{song, themeVideo} {
		if err := fixture.items.Upsert(ctx, value); err != nil {
			t.Fatalf("upsert %s: %v", value.ID, err)
		}
	}

	report := func(id dto.UUID, failed bool) playstate.Report {
		return playstate.Report{PlaybackState: session.PlaybackState{ItemID: id}, Failed: failed}
	}
	steps := []struct {
		start  bool
		report playstate.Report
	}{
		{true, report(fixture.item.ID, false)},
		{false, report(fixture.item.ID, true)},
		{true, report(song.ID, false)},
		{false, report(song.ID, false)},
		{true, report(themeVideo.ID, false)},
		{false, report(themeVideo.ID, false)},
		{true, report(dto.UUID{0x99}, false)},
	}
	for index, step := range steps {
		var err error
		if step.start {
			err = service.Start(ctx, fixture.actor, step.report)
		} else {
			err = service.Stop(ctx, fixture.actor, step.report)
		}
		if err != nil {
			t.Fatalf("step %d: %v", index, err)
		}
	}

	movie := activitylog.PlaybackItem{ID: fixture.item.ID, Name: "playstate fixture", MediaType: "Video"}
	songItem := activitylog.PlaybackItem{ID: song.ID, Name: "Song", Artists: song.Values.Artists, MediaType: "Audio"}
	movieStop, _ := activitylog.PlaybackStopped(fixture.primaryUser, "alice", movie, "Living Room")
	songStop, _ := activitylog.PlaybackStopped(fixture.primaryUser, "alice", songItem, "Living Room")
	want := []activitylog.Entry{
		activitylog.PlaybackStarted(fixture.primaryUser, "alice", movie, "Living Room"),
		movieStop,
		activitylog.PlaybackStarted(fixture.primaryUser, "alice", songItem, "Living Room"),
		songStop,
	}
	if len(activity.entries) != len(want) {
		t.Fatalf("entries = %+v; want %d", activity.entries, len(want))
	}
	for index, entry := range activity.entries {
		if entry.Name != want[index].Name || entry.Type != want[index].Type || entry.UserID != want[index].UserID || *entry.ItemID != *want[index].ItemID {
			t.Errorf("entry %d = %+v; want %+v", index, entry, want[index])
		}
	}
}

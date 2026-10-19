// SPDX-License-Identifier: GPL-2.0-only

package activitylog_test

import (
	"context"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/activitylog"
	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/events"
)

func TestLogPublishesCommittedEntryWithClockAndSequence(t *testing.T) {
	start := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	log := activitylog.New(clock.NewFake(start))
	t.Cleanup(func() { _ = log.Close(context.Background()) })
	subscription := log.Subscribe(events.SubscriptionOptions{Buffer: 1})

	written, err := log.Write(t.Context(), activitylog.Entry{Name: "Server started", Type: "ServerStarted"})
	if err != nil {
		t.Fatalf("write activity entry: %v", err)
	}
	if written.ID != 1 || !written.Date.Time.Equal(start) || written.Severity != "Information" {
		t.Fatalf("written activity = %+v, want id/date/default severity", written)
	}
	if got := receiveActivity(t, subscription.Events()); got != written {
		t.Fatalf("published activity = %+v, want %+v", got, written)
	}
}

func receiveActivity(t *testing.T, values <-chan activitylog.Entry) activitylog.Entry {
	t.Helper()
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	select {
	case value, ok := <-values:
		if !ok {
			t.Fatal("activity subscription closed")
		}
		return value
	case <-timer.C:
		t.Fatal("activity entry was not delivered")
		return activitylog.Entry{}
	}
}

// TestLogRecordWritesThroughTheWorker defends the queued path: Record stamps
// the entry at once, the worker persists it and publishes it, and Close writes
// the entries still queued.
func TestLogRecordWritesThroughTheWorker(t *testing.T) {
	start := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	c := clock.NewFake(start)
	log := activitylog.New(c)
	subscription := log.Subscribe(events.SubscriptionOptions{Buffer: 4})

	log.Record(activitylog.UserCreated(dto.UUID{1}, "alice"))
	got := receiveActivity(t, subscription.Events())
	if got.ID != 1 || got.Type != "UserCreated" || got.Name != "User alice has been created" || !got.Date.Time.Equal(start) {
		t.Fatalf("recorded entry = %+v", got)
	}

	c.Advance(time.Minute)
	log.Record(activitylog.UserDeleted("bob"))
	if err := log.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	page, err := log.Query(t.Context(), activitylog.Query{})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if page.TotalRecordCount != 2 || page.Entries[0].Type != "UserDeleted" || !page.Entries[0].Date.Time.Equal(start.Add(time.Minute)) {
		t.Fatalf("entries after Close = %+v", page)
	}
	log.Record(activitylog.UserDeleted("late"))
	if page, _ := log.Query(t.Context(), activitylog.Query{}); page.TotalRecordCount != 2 {
		t.Fatalf("Record after Close stored an entry: %+v", page)
	}
}

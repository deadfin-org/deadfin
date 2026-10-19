// SPDX-License-Identifier: GPL-2.0-only

package activitylog_test

import (
	"context"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/activitylog"
	"github.com/deadfin-org/deadfin/internal/clock"
)

// TestLogPruneDeletesThroughTheCut defends the CleanAsync cut: entries created
// at or before the cut are deleted (ActivityManager.cs, DateCreated <= startDate).
func TestLogPruneDeletesThroughTheCut(t *testing.T) {
	start := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	c := clock.NewFake(start)
	log := activitylog.New(c)
	t.Cleanup(func() { _ = log.Close(context.Background()) })

	old, err := log.Write(t.Context(), activitylog.Entry{Name: "old", Type: "Test"})
	if err != nil {
		t.Fatalf("write old entry: %v", err)
	}
	c.Advance(48 * time.Hour)
	recent, err := log.Write(t.Context(), activitylog.Entry{Name: "recent", Type: "Test"})
	if err != nil {
		t.Fatalf("write recent entry: %v", err)
	}

	removed, err := log.Prune(t.Context(), old.Date.Time)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if removed != 1 {
		t.Fatalf("Prune removed %d, want 1", removed)
	}
	page, err := log.Query(t.Context(), activitylog.Query{})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if page.TotalRecordCount != 1 || len(page.Entries) != 1 || page.Entries[0].ID != recent.ID {
		t.Fatalf("after Prune = %+v; want only %d", page, recent.ID)
	}
}

func TestLogPruneOnClosedLogReturnsError(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	log := activitylog.New(clock.NewFake(now))
	if err := log.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := log.Prune(context.Background(), now); err == nil {
		t.Fatal("Prune on closed log returned nil, want error")
	}
}

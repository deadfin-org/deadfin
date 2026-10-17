// SPDX-License-Identifier: GPL-2.0-only

package tasks_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/tasks"
)

type countingPeopleRefresher struct{ runs atomic.Int32 }

func (refresher *countingPeopleRefresher) Run(_ context.Context, progress func(float64)) error {
	refresher.runs.Add(1)
	progress(100)
	return nil
}

type scanRunning bool

func (running scanRunning) IsRunning(id string) bool {
	return bool(running) && id == tasks.RefreshLibraryID
}

// TestRefreshPeopleTaskRunsPeopleRefresher pins PeopleValidationTask: with a
// refresher the stock task is visible (IsHidden false) and runs it; without
// one it stays the hidden stub. A running library scan defers the task.
func TestRefreshPeopleTaskRunsPeopleRefresher(t *testing.T) {
	c := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	s, _, _, _ := newTestScheduler(t, c)
	if err := tasks.RegisterBuiltins(s, tasks.BuiltinDeps{}); err != nil {
		t.Fatalf("RegisterBuiltins: %v", err)
	}
	if info, ok := s.Get(tasks.RefreshPeopleID); !ok || !info.IsHidden {
		t.Fatalf("stub RefreshPeople = %+v, %v; want registered and hidden", info, ok)
	}

	for _, tc := range []struct {
		name     string
		scanning bool
		runs     int32
	}{
		{name: "idle library", runs: 1},
		{name: "scan running", scanning: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _, _ := newTestScheduler(t, c)
			refresher := &countingPeopleRefresher{}
			if err := tasks.RegisterBuiltins(s, tasks.BuiltinDeps{People: refresher, LibraryScanRunning: scanRunning(tc.scanning)}); err != nil {
				t.Fatalf("RegisterBuiltins: %v", err)
			}
			info, ok := s.Get(tasks.RefreshPeopleID)
			if !ok || info.IsHidden || info.Key != "RefreshPeople" || len(s.List()) != 12 {
				t.Fatalf("RefreshPeople = %+v, %v (count %d); want the visible stock task among 12", info, ok, len(s.List()))
			}
			if err := s.Run(tasks.RefreshPeopleID); err != nil {
				t.Fatalf("Run: %v", err)
			}
			waitForTaskIdle(t, s, tasks.RefreshPeopleID)
			if got := refresher.runs.Load(); got != tc.runs {
				t.Fatalf("refresher runs = %d, want %d", got, tc.runs)
			}
		})
	}
}

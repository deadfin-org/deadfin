// SPDX-License-Identifier: GPL-2.0-only

package tasks_test

import (
	"context"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/tasks"
)

// TestCleanActivityLogReadsTheConfiguredRetention defends CleanActivityLogTask:
// the cut is now minus ActivityLogRetentionDays, read at run time; a null or
// negative value fails the run without a prune; and OnCompleted receives each
// result, failed ones included (TaskCompletedLogger).
func TestCleanActivityLogReadsTheConfiguredRetention(t *testing.T) {
	c := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	s, _, _, _ := newTestScheduler(t, c)
	results := make(chan tasks.Result, 4)
	if err := s.OnCompleted(func(_ context.Context, result tasks.Result) { results <- result }); err != nil {
		t.Fatalf("OnCompleted: %v", err)
	}
	var cuts []time.Time
	retention := new(int)
	*retention = 7
	configured := retention
	if err := tasks.RegisterBuiltins(s, tasks.BuiltinDeps{
		ActivityLogPruner: testPruner{fn: func(_ context.Context, cut time.Time) (int, error) {
			cuts = append(cuts, cut)
			return 0, nil
		}},
		ActivityLogRetentionDays: func() *int { return configured },
		Clock:                    c,
		LogPath:                  t.TempDir(),
		CachePath:                t.TempDir(),
		TranscodePath:            t.TempDir(),
	}); err != nil {
		t.Fatal(err)
	}
	run := func() tasks.Result {
		t.Helper()
		if err := s.Run(tasks.CleanActivityLogID); err != nil {
			t.Fatal(err)
		}
		select {
		case result := <-results:
			return result
		case <-time.After(5 * time.Second):
			t.Fatal("no completion result")
			return tasks.Result{}
		}
	}

	if result := run(); result.Status != tasks.StatusCompleted {
		t.Fatalf("status = %s; want Completed", result.Status)
	}
	if len(cuts) != 1 || !cuts[0].Equal(c.Now().AddDate(0, 0, -7)) {
		t.Fatalf("cuts = %v; want one at now-7d", cuts)
	}
	negative := -1
	configured = &negative
	if result := run(); result.Status != tasks.StatusFailed || result.Name != "Clean Activity Log" || result.ErrorMessage == "" {
		t.Fatalf("negative retention result = %+v; want Failed", result)
	}
	configured = nil
	if result := run(); result.Status != tasks.StatusFailed {
		t.Fatalf("null retention status = %s; want Failed", result.Status)
	}
	if len(cuts) != 1 {
		t.Fatalf("invalid retention pruned: %v", cuts)
	}
}

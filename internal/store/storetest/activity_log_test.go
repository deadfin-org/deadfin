// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
	"github.com/deadfin-org/deadfin/internal/store/storetest"
)

func TestActivityLogStoreSQLite(t *testing.T) {
	stores, err := sqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("open SQLite: %v", err)
	}
	t.Cleanup(func() { _ = stores.Close() })
	testActivityLogStore(t, stores)
}

func TestActivityLogStorePostgres(t *testing.T) {
	testActivityLogStore(t, storetest.OpenPostgres(t))
}

// testActivityLogStore defends the persisted activity log: insert assigns
// increasing IDs, query filters on user presence, dates, item and severity,
// orders newest first with the ID tiebreak, pages with a total count, and
// DeleteThrough deletes rows at or before the cut (ActivityManager.cs).
func testActivityLogStore(t *testing.T, stores store.Stores) {
	t.Helper()
	ctx := t.Context()
	log := store.NewActivityLogStore(stores)
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	user := dto.UUID{0x11, 0x22}
	text := func(value string) *string { return &value }

	inputs := []store.ActivityLogEntry{
		{Name: "a", Type: "SessionStarted", UserID: user, ShortOverview: text("IP address: 10.0.0.1"), DateCreated: base, Severity: "Information"},
		{Name: "b", Type: "AuthenticationFailed", DateCreated: base.Add(time.Hour), Severity: "Error"},
		{Name: "c", Type: "VideoPlayback", UserID: user, ItemID: text("00112233445566778899aabbccddeeff"), DateCreated: base.Add(2 * time.Hour), Severity: "Information"},
		{Name: "d", Type: "TaskFailed", Overview: text("boom"), DateCreated: base.Add(2 * time.Hour), Severity: "Error"},
	}
	var lastID int64
	for _, input := range inputs {
		stored, err := log.Insert(ctx, input)
		if err != nil {
			t.Fatalf("Insert %s: %v", input.Name, err)
		}
		if stored.ID <= lastID {
			t.Fatalf("Insert %s ID = %d; want greater than %d", input.Name, stored.ID, lastID)
		}
		lastID = stored.ID
	}
	if _, err := log.Insert(ctx, store.ActivityLogEntry{Type: "x", Severity: "Information"}); err == nil {
		t.Fatal("Insert without a name succeeded")
	}

	names := func(page store.ActivityLogPage) []string {
		result := make([]string, 0, len(page.Entries))
		for _, entry := range page.Entries {
			result = append(result, entry.Name)
		}
		return result
	}
	yes, no := true, false
	two := 2
	minDate, maxDate := base.Add(time.Hour), base.Add(time.Hour)
	for _, tc := range []struct {
		name  string
		query store.ActivityLogQuery
		want  []string
		total int
	}{
		{name: "all newest first with id tiebreak", want: []string{"d", "c", "b", "a"}, total: 4},
		{name: "has user", query: store.ActivityLogQuery{HasUserID: &yes}, want: []string{"c", "a"}, total: 2},
		{name: "no user", query: store.ActivityLogQuery{HasUserID: &no}, want: []string{"d", "b"}, total: 2},
		{name: "min date inclusive", query: store.ActivityLogQuery{MinDate: &minDate}, want: []string{"d", "c", "b"}, total: 3},
		{name: "max date inclusive", query: store.ActivityLogQuery{MaxDate: &maxDate}, want: []string{"b", "a"}, total: 2},
		{name: "item", query: store.ActivityLogQuery{ItemID: text("00112233445566778899aabbccddeeff")}, want: []string{"c"}, total: 1},
		{name: "severity", query: store.ActivityLogQuery{Severity: text("Error")}, want: []string{"d", "b"}, total: 2},
		{name: "page", query: store.ActivityLogQuery{StartIndex: 1, Limit: &two}, want: []string{"c", "b"}, total: 4},
		{name: "skip without limit", query: store.ActivityLogQuery{StartIndex: 3}, want: []string{"a"}, total: 4},
	} {
		page, err := log.Query(ctx, tc.query)
		if err != nil {
			t.Fatalf("%s: Query: %v", tc.name, err)
		}
		if got := names(page); !equalStrings(got, tc.want) || page.TotalRecordCount != tc.total {
			t.Fatalf("%s: got %v total %d; want %v total %d", tc.name, got, page.TotalRecordCount, tc.want, tc.total)
		}
	}

	page, err := log.Query(ctx, store.ActivityLogQuery{HasUserID: &yes})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	got := page.Entries[1]
	if got.UserID != user || got.ShortOverview == nil || *got.ShortOverview != "IP address: 10.0.0.1" ||
		got.Overview != nil || got.ItemID != nil || !got.DateCreated.Equal(base) || got.Type != "SessionStarted" || got.Severity != "Information" {
		t.Fatalf("round trip = %+v", got)
	}

	removed, err := log.DeleteThrough(ctx, base.Add(time.Hour))
	if err != nil {
		t.Fatalf("DeleteThrough: %v", err)
	}
	if removed != 2 {
		t.Fatalf("DeleteThrough removed %d; want 2", removed)
	}
	page, err = log.Query(ctx, store.ActivityLogQuery{})
	if err != nil {
		t.Fatalf("Query after delete: %v", err)
	}
	if got := names(page); !equalStrings(got, []string{"d", "c"}) {
		t.Fatalf("after delete = %v; want [d c]", got)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for index := range a {
		if a[index] != b[index] {
			return false
		}
	}
	return true
}

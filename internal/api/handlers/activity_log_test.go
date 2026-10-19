// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/activitylog"
	"github.com/deadfin-org/deadfin/internal/api/handlers"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/store"
)

// stubActivityLog is a test-only ActivityLogReader. Query loads the entries
// into a process-local activitylog.Log and queries it.
type stubActivityLog struct {
	entries []activitylog.Entry
	queries []activitylog.Query
}

func (s *stubActivityLog) Query(ctx context.Context, query activitylog.Query) (activitylog.Page, error) {
	s.queries = append(s.queries, query)
	log := activitylog.New(clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)))
	defer func() { _ = log.Close(context.Background()) }()
	for _, entry := range s.entries {
		if _, err := log.Write(ctx, entry); err != nil {
			return activitylog.Page{}, err
		}
	}
	return log.Query(ctx, query)
}

// stubUserResolver is a test-only ItemUserResolver.
type stubUserResolver struct {
	users map[dto.UUID]store.User
	err   error
}

func (s *stubUserResolver) Get(_ context.Context, id dto.UUID) (store.User, error) {
	if s.err != nil {
		return store.User{}, s.err
	}
	u, ok := s.users[id]
	if !ok {
		return store.User{}, store.ErrNotFound
	}
	return u, nil
}

func TestActivityLogEntriesRouteRegistration(t *testing.T) {
	rt := router.New()
	handlers.RegisterActivityLog(rt, &stubActivityLog{}, &stubUserResolver{})

	got, found := routeByOperation(rt.Routes(), "GetLogEntries")
	if !found {
		t.Fatal("GetLogEntries is not registered")
	}
	wantMethod := http.MethodGet
	wantPattern := "/System/ActivityLog/Entries"
	wantPolicy := string(auth.PolicyRequiresElevation)
	if got.Method != wantMethod || got.Pattern != wantPattern || got.Policy != wantPolicy {
		t.Fatalf("GetLogEntries route = {Method: %q, Pattern: %q, Policy: %q}, want {Method: %q, Pattern: %q, Policy: %q}",
			got.Method, got.Pattern, got.Policy, wantMethod, wantPattern, wantPolicy)
	}
	if got.Anonymous {
		t.Fatal("GetLogEntries route must not be anonymous")
	}
}

func TestActivityLogEntriesNilLogReturns503(t *testing.T) {
	rt := router.New()
	handlers.RegisterActivityLog(rt, nil, nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/System/ActivityLog/Entries", nil)
	rt.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("nil log status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

func TestActivityLogEntriesEmptyLogReturnsEmptyResult(t *testing.T) {
	rt := router.New()
	log := &stubActivityLog{}
	handlers.RegisterActivityLog(rt, log, nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/System/ActivityLog/Entries", nil)
	rt.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var result map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode response: %v; body: %s", err, rec.Body.String())
	}
	if result["Items"] == nil {
		t.Fatal("Items must be an allocated empty array, not null")
	}
	if items, ok := result["Items"].([]any); !ok || len(items) != 0 {
		t.Fatalf("Items = %v, want empty array", result["Items"])
	}
	if result["TotalRecordCount"].(float64) != 0 {
		t.Fatalf("TotalRecordCount = %v, want 0", result["TotalRecordCount"])
	}
	if result["StartIndex"].(float64) != 0 {
		t.Fatalf("StartIndex = %v, want 0", result["StartIndex"])
	}
}

func TestActivityLogEntriesPagination(t *testing.T) {
	log := &stubActivityLog{}
	for range 150 {
		log.entries = append(log.entries, activitylog.Entry{
			Name:     "Test Event",
			Type:     "Test",
			Severity: "Information",
		})
	}

	rt := router.New()
	handlers.RegisterActivityLog(rt, log, nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/System/ActivityLog/Entries", nil)
	rt.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var result struct {
		Items            []any `json:"Items"`
		TotalRecordCount int   `json:"TotalRecordCount"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode: %v; body: %s", err, rec.Body.String())
	}
	if len(result.Items) != 100 {
		t.Fatalf("Items count = %d, want 100 (default limit)", len(result.Items))
	}
	if result.TotalRecordCount != 150 {
		t.Fatalf("TotalRecordCount = %d, want 150", result.TotalRecordCount)
	}
	// A request without text filters or sorts pages in the store.
	if len(log.queries) != 1 || log.queries[0].Limit == nil || *log.queries[0].Limit != 100 || log.queries[0].StartIndex != 0 {
		t.Fatalf("store queries = %+v; want one paged query with limit 100", log.queries)
	}
}

func TestActivityLogEntriesFilters(t *testing.T) {
	userID := dto.UUID{1}
	otherID := dto.UUID{2}
	itemIDStr := "00000000000000000000000000000001"

	log := &stubActivityLog{
		entries: []activitylog.Entry{
			{Name: "Login", Type: "Session", Severity: "Information", UserID: userID, Overview: activityLogString("User signed in to the dashboard"), ShortOverview: activityLogString("Signed in"), Date: dto.NewTime(time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC))},
			{Name: "Scan", Type: "Library", Severity: "Warning", UserID: dto.NilUUID, Date: dto.NewTime(time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC))},
			{Name: "Error", Type: "System", Severity: "Error", UserID: otherID, ItemID: &itemIDStr, Overview: activityLogString("Playback failed for the item"), ShortOverview: activityLogString("Failure"), Date: dto.NewTime(time.Date(2026, 1, 5, 12, 0, 0, 0, time.UTC))},
		},
	}
	users := &stubUserResolver{
		users: map[dto.UUID]store.User{
			userID:  {ID: userID, Name: "Alice"},
			otherID: {ID: otherID, Name: "Bob"},
		},
	}

	tests := []struct {
		name      string
		query     string
		wantNames []string
	}{
		{"hasUserId true", "?hasUserId=true", []string{"Login", "Error"}},
		{"hasUserId false", "?hasUserId=false", []string{"Scan"}},
		{"minDate", "?minDate=2026-01-12T00:00:00Z", []string{"Login"}},
		{"maxDate", "?maxDate=2026-01-08T00:00:00Z", []string{"Error"}},
		{"name", "?name=log", []string{"Login"}},
		{"overview", "?overview=signed", []string{"Login"}},
		{"shortOverview", "?shortOverview=sign", []string{"Login"}},
		{"type", "?type=library", []string{"Scan"}},
		{"severity", "?severity=Error", []string{"Error"}},
		{"itemId", "?itemId=00000000000000000000000000000001", []string{"Error"}},
		{"username", "?username=alice", []string{"Login"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rt := router.New()
			handlers.RegisterActivityLog(rt, log, users)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/System/ActivityLog/Entries"+tt.query, nil)
			rt.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
			}
			var result struct {
				Items []struct {
					Name string `json:"Name"`
				} `json:"Items"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
				t.Fatalf("decode: %v; body: %s", err, rec.Body.String())
			}
			if len(result.Items) != len(tt.wantNames) {
				t.Fatalf("Items count = %d, want %d; got: %+v", len(result.Items), len(tt.wantNames), result.Items)
			}
			for i, want := range tt.wantNames {
				if result.Items[i].Name != want {
					t.Fatalf("Items[%d].Name = %q, want %q", i, result.Items[i].Name, want)
				}
			}
		})
	}
}

func TestActivityLogEntriesSorting(t *testing.T) {
	aliceID := dto.UUID{1}
	bobID := dto.UUID{2}
	log := &stubActivityLog{
		entries: []activitylog.Entry{
			{Name: "Alpha", Type: "T", Severity: "Information", UserID: aliceID, Date: dto.NewTime(time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))},
			{Name: "Beta", Type: "T", Severity: "Information", UserID: bobID, Date: dto.NewTime(time.Date(2026, 1, 3, 12, 0, 0, 0, time.UTC))},
			{Name: "Gamma", Type: "T", Severity: "Information", UserID: dto.NilUUID, Date: dto.NewTime(time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC))},
		},
	}
	users := &stubUserResolver{
		users: map[dto.UUID]store.User{
			aliceID: {ID: aliceID, Name: "Alice"},
			bobID:   {ID: bobID, Name: "Bob"},
		},
	}

	tests := []struct {
		name      string
		query     string
		wantOrder []string
	}{
		// No sortBy: Date descending (newest first). This is the spec default
		// (ActivityManager.cs:164-167); the snapshot order does not matter
		// because the handler sorts explicitly.
		{"default date descending", "", []string{"Beta", "Gamma", "Alpha"}},
		{"ascending date", "?sortBy=DateCreated&sortOrder=Ascending", []string{"Alpha", "Gamma", "Beta"}},
		// Username sorts by the resolved user name. Ascending places entries
		// with no resolvable user first (SQL NULL-first), then by name, so
		// Alice (Alpha) comes before Bob (Beta).
		{"ascending username", "?sortBy=Username&sortOrder=Ascending", []string{"Gamma", "Alpha", "Beta"}},
		// Descending username places entries with no resolvable user last.
		{"descending username", "?sortBy=Username&sortOrder=Descending", []string{"Beta", "Alpha", "Gamma"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rt := router.New()
			handlers.RegisterActivityLog(rt, log, users)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/System/ActivityLog/Entries"+tt.query, nil)
			rt.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
			}
			var result struct {
				Items []struct {
					Name string `json:"Name"`
				} `json:"Items"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
				t.Fatalf("decode: %v; body: %s", err, rec.Body.String())
			}
			if len(result.Items) != len(tt.wantOrder) {
				t.Fatalf("Items count = %d, want %d; got: %+v", len(result.Items), len(tt.wantOrder), result.Items)
			}
			for i, want := range tt.wantOrder {
				if result.Items[i].Name != want {
					t.Fatalf("Items[%d].Name = %q, want %q", i, result.Items[i].Name, want)
				}
			}
		})
	}
}

func TestActivityLogEntriesInvalidSeverityReturns400(t *testing.T) {
	rt := router.New()
	handlers.RegisterActivityLog(rt, &stubActivityLog{}, nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/System/ActivityLog/Entries?severity=Bogus", nil)
	rt.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid severity status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestActivityLogEntriesNegativeLimitReturns400(t *testing.T) {
	rt := router.New()
	handlers.RegisterActivityLog(rt, &stubActivityLog{}, nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/System/ActivityLog/Entries?limit=-1", nil)
	rt.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("negative limit status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestActivityLogEntriesNegativeStartIndexReturns400(t *testing.T) {
	rt := router.New()
	handlers.RegisterActivityLog(rt, &stubActivityLog{}, nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/System/ActivityLog/Entries?startIndex=-5", nil)
	rt.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("negative startIndex status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestActivityLogEntriesInvalidSortByReturns400(t *testing.T) {
	rt := router.New()
	handlers.RegisterActivityLog(rt, &stubActivityLog{}, nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/System/ActivityLog/Entries?sortBy=Bogus", nil)
	rt.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid sortBy status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestActivityLogEntriesInvalidSortOrderReturns400(t *testing.T) {
	rt := router.New()
	handlers.RegisterActivityLog(rt, &stubActivityLog{}, nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/System/ActivityLog/Entries?sortOrder=Sideways", nil)
	rt.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid sortOrder status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func activityLogString(value string) *string { return &value }

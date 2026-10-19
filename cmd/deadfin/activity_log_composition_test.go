// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
)

// TestApplicationRecordsAndPersistsActivity defends the composed activity
// log: the startup password, a failed login, and a successful login write
// their upstream entries, and the entries survive an application restart.
func TestApplicationRecordsAndPersistsActivity(t *testing.T) {
	root := filepath.Join(t.TempDir(), "program-data")
	first, err := newApplicationWithListen(t.Context(), root, defaultListenAddress)
	if err != nil {
		t.Fatalf("open first application: %v", err)
	}
	configureComposedAdministrator(t, first.Router())
	rec := authenticatedCompositionRequest(first.Router(), http.MethodPost, "/Users/AuthenticateByName", []byte(`{"Username":"captain","Pw":"wrong"}`), "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password status = %d; want 401", rec.Code)
	}
	_ = loginComposedAdministrator(t, first.Router())
	if err := first.Close(context.Background()); err != nil {
		t.Fatalf("close first application: %v", err)
	}

	second := newCompositionApplication(t, root)
	token := loginComposedAdministrator(t, second.Router())
	type entry struct {
		Name          string
		Type          string
		UserID        string `json:"UserId"`
		ShortOverview string
		Severity      string
	}
	read := func(query string) (entries []entry, total int) {
		t.Helper()
		rec := authenticatedCompositionRequest(second.Router(), http.MethodGet, "/System/ActivityLog/Entries"+query, nil, token)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET entries%s status = %d; body %s", query, rec.Code, rec.Body.String())
		}
		var page struct {
			Items            []entry
			TotalRecordCount int
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
			t.Fatalf("decode entries: %v", err)
		}
		return page.Items, page.TotalRecordCount
	}

	failed, _ := read("?hasUserId=false")
	if len(failed) != 1 || failed[0].Type != "AuthenticationFailed" || failed[0].Name != "Failed login attempt from captain" || failed[0].Severity != "Error" {
		t.Fatalf("entries without user = %+v; want one AuthenticationFailed", failed)
	}
	// The type filter takes the in-memory filter path over the store rows.
	withUser, _ := read("?hasUserId=true&type=a")
	seen := map[string]int{}
	for _, value := range withUser {
		seen[value.Type]++
	}
	for _, kind := range []string{"UserPasswordChanged", "SessionStarted", "AuthenticationSucceeded"} {
		if seen[kind] == 0 {
			t.Fatalf("persisted entries with user = %+v; missing %s", withUser, kind)
		}
	}
	if _, total := read("?startIndex=0&limit=1"); total < 4 {
		t.Fatalf("total = %d; want at least the 4 entries written before the restart", total)
	}
}

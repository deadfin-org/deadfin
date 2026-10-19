// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/api/handlers"
	"github.com/deadfin-org/deadfin/internal/api/middleware"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
)

// GetServerLogs and GetLogFile are ported from Jellyfin.Api/Controllers/
// SystemController.cs:146-176 and 206-225.
func newSystemLogsRouter(t *testing.T, identity auth.Identity, directory string) *router.Router {
	t.Helper()
	_, manager, _ := newManagedRouter(t)
	evaluator := auth.NewEvaluator(manager, startupUserLocalNetwork{}, startupUserSchedules{}, func() time.Time { return time.Unix(0, 0).UTC() })
	authorizer := middleware.NewAuthorizer(configurationResolver{identity: identity}, evaluator, configurationUsers{})
	rt := router.New(authorizer.Wrap)
	handlers.RegisterSystemLogs(rt, directory)
	return rt
}

func writeLogFixture(t *testing.T, path, content string, modified time.Time) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if err := os.Chtimes(path, modified, modified); err != nil {
		t.Fatalf("set times of %s: %v", path, err)
	}
}

func TestSystemLogsListsLogFilesNewestFirst(t *testing.T) {
	directory := t.TempDir()
	base := time.Date(2026, time.October, 1, 8, 0, 0, 0, time.UTC)
	writeLogFixture(t, filepath.Join(directory, "FFmpeg.Transcode-old.log"), "old", base)
	writeLogFixture(t, filepath.Join(directory, "upload_10.11.0_web_abc.TXT"), "client log", base.Add(2*time.Hour))
	writeLogFixture(t, filepath.Join(directory, "FFmpeg.Transcode-new.log"), "newest line\n", base.Add(3*time.Hour))
	writeLogFixture(t, filepath.Join(directory, "notes.md"), "not a log", base.Add(4*time.Hour))
	if err := os.Mkdir(filepath.Join(directory, "nested.log"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeLogFixture(t, filepath.Join(directory, "nested.log", "inner.log"), "nested", base.Add(5*time.Hour))

	rt := newSystemLogsRouter(t, adminConfigUser(), directory)
	rec := request(rt, http.MethodGet, "/System/Logs", "", "", "")
	assertStatus(t, rec, http.StatusOK)
	var got []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v; body=%q", err, rec.Body.String())
	}
	var names []string
	for _, file := range got {
		names = append(names, file["Name"].(string))
	}
	if want := []string{"FFmpeg.Transcode-new.log", "upload_10.11.0_web_abc.TXT", "FFmpeg.Transcode-old.log"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("log names = %v, want %v", names, want)
	}
	if got[0]["DateModified"] != "2026-10-01T11:00:00.0000000Z" || got[0]["Size"] != float64(len("newest line\n")) {
		t.Fatalf("newest log = %#v", got[0])
	}
	if _, ok := got[0]["DateCreated"].(string); !ok {
		t.Fatalf("DateCreated missing: %#v", got[0])
	}

	rec = request(rt, http.MethodGet, "/System/Logs/Log?name=ffmpeg.transcode-NEW.log", "", "", "")
	assertStatus(t, rec, http.StatusOK)
	if ct := rec.Header().Get("Content-Type"); ct != "text/plain; charset=utf-8" {
		t.Fatalf("Content-Type = %q", ct)
	}
	if rec.Body.String() != "newest line\n" {
		t.Fatalf("log body = %q", rec.Body.String())
	}
	// Any top-level file is served, also one the list does not show.
	assertStatus(t, request(rt, http.MethodGet, "/System/Logs/Log?name=notes.md", "", "", ""), http.StatusOK)
	for _, name := range []string{"missing.log", "nested.log", "nested.log/inner.log", "../" + filepath.Base(directory) + "/notes.md"} {
		rec = request(rt, http.MethodGet, "/System/Logs/Log?name="+name, "", "", "")
		assertStatus(t, rec, http.StatusNotFound)
		if rec.Body.String() != `"Log file not found."` {
			t.Fatalf("%s body = %q", name, rec.Body.String())
		}
	}
	assertStatus(t, request(rt, http.MethodGet, "/System/Logs/Log", "", "", ""), http.StatusBadRequest)
}

func TestSystemLogsMissingDirectoryIsEmpty(t *testing.T) {
	rt := newSystemLogsRouter(t, adminConfigUser(), filepath.Join(t.TempDir(), "absent"))
	rec := request(rt, http.MethodGet, "/System/Logs", "", "", "")
	assertStatus(t, rec, http.StatusOK)
	if rec.Body.String() != "[]" {
		t.Fatalf("body = %q, want []", rec.Body.String())
	}
}

func TestSystemLogsRequireElevation(t *testing.T) {
	rt := newSystemLogsRouter(t, authenticatedConfigUser(), t.TempDir())
	assertStatus(t, request(rt, http.MethodGet, "/System/Logs", "", "", ""), http.StatusForbidden)
	assertStatus(t, request(rt, http.MethodGet, "/System/Logs/Log?name=a.log", "", "", ""), http.StatusForbidden)
}

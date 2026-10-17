// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/api/handlers"
	"github.com/deadfin-org/deadfin/internal/api/middleware"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/dto"
)

var clientLogNow = time.Date(2026, 10, 16, 21, 4, 5, 0, time.FixedZone("EDT", -4*3600))

type clientLogFixture struct {
	rt      *router.Router
	dir     string
	enabled bool
}

func newClientLogFixture(t *testing.T, identity auth.Identity) *clientLogFixture {
	t.Helper()
	_, manager, _ := newManagedRouter(t)
	evaluator := auth.NewEvaluator(manager, startupUserLocalNetwork{}, startupUserSchedules{}, func() time.Time { return time.Unix(0, 0).UTC() })
	authorizer := middleware.NewAuthorizer(configurationResolver{identity: identity}, evaluator, configurationUsers{})
	f := &clientLogFixture{rt: router.New(authorizer.Wrap), dir: filepath.Join(t.TempDir(), "log"), enabled: true}
	handlers.RegisterClientLog(f.rt, handlers.ClientLogConfig{
		Enabled:      func() bool { return f.enabled },
		LogDirectory: f.dir,
		Clock:        clock.NewFake(clientLogNow),
		Random:       bytes.NewReader(bytes.Repeat([]byte{0xab}, 64)),
	})
	return f
}

func (f *clientLogFixture) post(body io.Reader, contentLength int64) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/ClientLog/Document", body)
	request.Header.Set("Content-Type", "text/plain")
	request.ContentLength = contentLength
	rec := httptest.NewRecorder()
	f.rt.ServeHTTP(rec, request)
	return rec
}

func (f *clientLogFixture) files(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(f.dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func clientLogUser(client, version string) auth.Identity {
	identity := authenticatedConfigUser()
	identity.Client = client
	identity.Version = version
	return identity
}

func TestClientLogRouteRegisteredWithDefaultAuthorization(t *testing.T) {
	f := newClientLogFixture(t, clientLogUser("SharkParty", "1.0"))
	for _, route := range f.rt.Routes() {
		if route.Method == http.MethodPost && route.Pattern == "/ClientLog/Document" {
			if route.OperationID != "LogFile" || route.Policy != string(auth.PolicyDefaultAuthorization) || route.Anonymous {
				t.Fatalf("route = %+v, want LogFile/DefaultAuthorization", route)
			}
			return
		}
	}
	t.Fatal("POST /ClientLog/Document not registered")
}

// TestClientLogWritesDocument pins ClientEventLogger.WriteDocumentAsync: the
// body is written verbatim to
// upload_{client}_{version}_{yyyyMMddHHmmss UTC}_{Guid:N}.log in the log
// directory, and the response is ClientLogDocumentResponseDto.
func TestClientLogWritesDocument(t *testing.T) {
	f := newClientLogFixture(t, clientLogUser("SharkParty", "1.2.3"))
	payload := "line one\nline two\n"
	rec := f.post(strings.NewReader(payload), int64(len(payload)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	want := "upload_SharkParty_1.2.3_20261017010405_abababababababababababababababab.log"
	if got := rec.Body.String(); got != `{"FileName":"`+want+`"}` {
		t.Fatalf("body = %s, want FileName %s", got, want)
	}
	contents, err := os.ReadFile(filepath.Join(f.dir, want))
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != payload {
		t.Fatalf("file = %q, want %q", contents, payload)
	}
}

func TestClientLogCamelCaseResponse(t *testing.T) {
	f := newClientLogFixture(t, clientLogUser("SharkParty", "1.0"))
	request := httptest.NewRequest(http.MethodPost, "/ClientLog/Document", strings.NewReader("x"))
	request.Header.Set("Accept", "application/json; profile=\"CamelCase\"")
	rec := httptest.NewRecorder()
	f.rt.ServeHTTP(rec, request)
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body["fileName"] == "" {
		t.Fatalf("status %d body %s, want camelCase fileName", rec.Code, rec.Body.String())
	}
}

// TestClientLogNames covers GetRequestInformation and GetSafeLeafFileName:
// missing values fall back, an API key reports "apikey", and path segments
// never leave the log directory.
func TestClientLogNames(t *testing.T) {
	apiKey := clientLogUser("Coordinator", "9.9")
	apiKey.APIKey = true
	for _, tc := range []struct {
		name     string
		identity auth.Identity
		prefix   string
	}{
		{"missing", clientLogUser("", ""), "upload_unknown-client_unknown-version_"},
		{"api key", apiKey, "upload_Coordinator_apikey_"},
		{"traversal client", clientLogUser("../../../../etc/passwd", "1.0"), "upload_passwd_1.0_"},
		{"traversal version", clientLogUser("normal-client", "../../../etc/passwd"), "upload_normal-client_passwd_"},
		{"absolute", clientLogUser("/absolute/path", "1.0"), "upload_path_1.0_"},
		{"trailing slash", clientLogUser("client/", ".."), "upload_unknown-client_unknown-version_"},
		{"backslashes", clientLogUser(`..\..\windows\system32`, "1.0"), `upload_..\..\windows\system32_1.0_`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newClientLogFixture(t, tc.identity)
			rec := f.post(strings.NewReader("payload"), 7)
			var body dto.ClientLogDocumentResponseDto
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || rec.Code != http.StatusOK {
				t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
			}
			if !strings.HasPrefix(body.FileName, tc.prefix) {
				t.Fatalf("FileName = %q, want prefix %q", body.FileName, tc.prefix)
			}
			if files := f.files(t); len(files) != 1 || files[0] != body.FileName {
				t.Fatalf("log dir = %v, want only %s", files, body.FileName)
			}
		})
	}
}

func TestClientLogDisabledIsForbidden(t *testing.T) {
	f := newClientLogFixture(t, clientLogUser("SharkParty", "1.0"))
	f.enabled = false
	rec := f.post(strings.NewReader("payload"), 7)
	if rec.Code != http.StatusForbidden || rec.Body.Len() != 0 {
		t.Fatalf("status = %d body %q, want empty 403", rec.Code, rec.Body.String())
	}
	if files := f.files(t); len(files) != 0 {
		t.Fatalf("log dir = %v, want empty", files)
	}
}

func TestClientLogUnauthenticated(t *testing.T) {
	f := newClientLogFixture(t, auth.Identity{})
	if rec := f.post(strings.NewReader("payload"), 7); rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

// TestClientLogSizeLimit pins MaxDocumentSize = 1,000,000: exactly the limit
// is accepted; a larger declared Content-Length or a larger chunked body
// answers 413 and leaves no file.
func TestClientLogSizeLimit(t *testing.T) {
	const limit = 1_000_000
	t.Run("at limit", func(t *testing.T) {
		f := newClientLogFixture(t, clientLogUser("SharkParty", "1.0"))
		if rec := f.post(bytes.NewReader(make([]byte, limit)), limit); rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
	})
	for _, tc := range []struct {
		name   string
		length int64
	}{{"declared", limit + 1}, {"chunked", -1}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newClientLogFixture(t, clientLogUser("SharkParty", "1.0"))
			rec := f.post(bytes.NewReader(make([]byte, limit+1)), tc.length)
			if rec.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("status = %d, want 413", rec.Code)
			}
			if got := rec.Body.String(); got != `"Payload must be less than 1,000,000 bytes"` {
				t.Fatalf("body = %s", got)
			}
			if files := f.files(t); len(files) != 0 {
				t.Fatalf("log dir = %v, want no partial file", files)
			}
		})
	}
}

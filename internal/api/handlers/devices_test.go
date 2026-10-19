// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/api/handlers"
	"github.com/deadfin-org/deadfin/internal/api/middleware"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/session"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
	"github.com/deadfin-org/deadfin/internal/store/storetest"
	"github.com/deadfin-org/deadfin/internal/user"
)

// The Devices controller is ported from Jellyfin.Api/Controllers/
// DevicesController.cs. These tests run the handlers on the real identity
// store, on SQLite and, when DEADFIN_TEST_POSTGRES_DSN is set, on PostgreSQL.
func TestDevicesSQLite(t *testing.T) {
	stores, err := sqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("open SQLite: %v", err)
	}
	t.Cleanup(func() { _ = stores.Close() })
	runDevicesContract(t, stores)
}

func TestDevicesPostgres(t *testing.T) {
	runDevicesContract(t, storetest.OpenPostgres(t))
}

type devicesFixture struct {
	rt       *router.Router
	identity *store.Identity
	sessions *session.Manager
	admin    store.User
	viewer   store.User
}

func newDevicesFixture(t *testing.T, stores store.Stores, administrator bool) devicesFixture {
	t.Helper()
	identity := store.NewIdentity(stores)
	fakeClock := clock.NewFake(time.Date(2026, time.October, 19, 12, 0, 0, 0, time.UTC))
	users := user.New(identity, fakeClock, &p104Entropy{})
	admin := mustCreateDeviceUser(t, users, "admin", `{"IsAdministrator":true}`)
	viewer := mustCreateDeviceUser(t, users, "viewer", `{"IsAdministrator":false,"EnableAllDevices":false,"EnabledDevices":["DEV-B"]}`)

	base := time.Date(2026, time.October, 1, 8, 0, 0, 0, time.UTC)
	for _, device := range []store.Device{
		{UserID: admin.ID, AccessToken: "token-admin-a", AppName: "Jellyfin Web", AppVersion: "10.11.0", DeviceName: "Firefox", DeviceID: "dev-a", DateLastActivity: base.Add(time.Hour)},
		{UserID: viewer.ID, AccessToken: "token-viewer-b", AppName: "Swiftfin", AppVersion: "1.2", DeviceName: "Apple TV", DeviceID: "dev-b", DateLastActivity: base.Add(3 * time.Hour)},
		{UserID: admin.ID, AccessToken: "token-admin-b", AppName: "Swiftfin", AppVersion: "1.1", DeviceName: "Apple TV", DeviceID: "dev-b", DateLastActivity: base.Add(2 * time.Hour)},
		{UserID: viewer.ID, AccessToken: "token-viewer-c", AppName: "Infuse", AppVersion: "8", DeviceName: "iPad", DeviceID: "dev-c", DateLastActivity: base},
	} {
		device.DateCreated = base
		device.DateModified = base
		if _, err := identity.CreateDevice(t.Context(), device); err != nil {
			t.Fatalf("create device %s: %v", device.AccessToken, err)
		}
	}

	_, manager, _ := newManagedRouter(t)
	caller := auth.Identity{Authenticated: true, Administrator: administrator, UserID: admin.ID}
	if !administrator {
		caller.UserID = viewer.ID
	}
	evaluator := auth.NewEvaluator(manager, startupUserLocalNetwork{}, startupUserSchedules{}, func() time.Time { return time.Unix(0, 0).UTC() })
	authorizer := middleware.NewAuthorizer(configurationResolver{identity: caller}, evaluator, configurationUsers{})
	rt := router.New(authorizer.Wrap)
	sessions := session.New(fakeClock)
	t.Cleanup(func() { _ = sessions.Close() })
	handlers.RegisterDevices(rt, users, sessions, nil)
	return devicesFixture{rt: rt, identity: identity, sessions: sessions, admin: admin, viewer: viewer}
}

func mustCreateDeviceUser(t *testing.T, users *user.Service, name, policy string) store.User {
	t.Helper()
	created, err := users.Create(t.Context(), name, "password")
	if err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
	created.Policy = json.RawMessage(policy)
	if err := users.Update(t.Context(), created); err != nil {
		t.Fatalf("update %s policy: %v", name, err)
	}
	return created
}

func runDevicesContract(t *testing.T, stores store.Stores) {
	f := newDevicesFixture(t, stores, true)

	t.Run("list orders by activity and omits the token", func(t *testing.T) {
		got := decodeDevicesBody[dto.QueryResult[map[string]any]](t, request(f.rt, http.MethodGet, "/Devices", "", "", ""), http.StatusOK)
		if got.TotalRecordCount != 4 || got.StartIndex != 0 {
			t.Fatalf("TotalRecordCount/StartIndex = %d/%d, want 4/0", got.TotalRecordCount, got.StartIndex)
		}
		var order []string
		for _, entry := range got.Items {
			order = append(order, entry["LastUserName"].(string)+"/"+entry["Id"].(string))
		}
		if want := []string{"viewer/dev-b", "admin/dev-b", "admin/dev-a", "viewer/dev-c"}; !reflect.DeepEqual(order, want) {
			t.Fatalf("device order = %v, want %v", order, want)
		}
		want := map[string]any{
			"Name":             "Apple TV",
			"Id":               "dev-b",
			"LastUserName":     "viewer",
			"AppName":          "Swiftfin",
			"AppVersion":       "1.2",
			"LastUserId":       f.viewer.ID.String(),
			"DateLastActivity": "2026-10-01T11:00:00.0000000Z",
			"Capabilities": map[string]any{
				"PlayableMediaTypes":           []any{},
				"SupportedCommands":            []any{},
				"SupportsMediaControl":         false,
				"SupportsPersistentIdentifier": true,
			},
		}
		if !reflect.DeepEqual(got.Items[0], want) {
			t.Fatalf("first device = %#v, want %#v", got.Items[0], want)
		}
	})

	t.Run("list for a user applies the device policy", func(t *testing.T) {
		path := "/Devices?userId=" + f.viewer.ID.String()
		got := decodeDevicesBody[dto.QueryResult[map[string]any]](t, request(f.rt, http.MethodGet, path, "", "", ""), http.StatusOK)
		if ids := deviceIDs(got.Items); !reflect.DeepEqual(ids, []string{"dev-b", "dev-b"}) {
			t.Fatalf("viewer devices = %v, want both dev-b rows", ids)
		}
		// A device that reports no persistent identifier is always allowed
		// (DeviceManager.CanAccessDevice).
		f.sessions.RecordAuthenticatedActivity(session.Activity{Client: "Infuse", DeviceID: "dev-c", DeviceName: "iPad", UserID: f.viewer.ID})
		snapshot, ok := f.sessions.Session("Infuse", "dev-c")
		if !ok {
			t.Fatal("dev-c session was not created")
		}
		if err := f.sessions.ReportCapabilities(t.Context(), session.CapabilityReport{CurrentID: snapshot.ID}); err != nil {
			t.Fatalf("report capabilities: %v", err)
		}
		got = decodeDevicesBody[dto.QueryResult[map[string]any]](t, request(f.rt, http.MethodGet, path, "", "", ""), http.StatusOK)
		if ids := deviceIDs(got.Items); !reflect.DeepEqual(ids, []string{"dev-b", "dev-b", "dev-c"}) {
			t.Fatalf("viewer devices = %v, want dev-c added", ids)
		}
		rec := request(f.rt, http.MethodGet, "/Devices?userId=0123456789abcdef0123456789abcdef", "", "", "")
		assertStatus(t, rec, http.StatusNotFound)
	})

	t.Run("info returns the newest row of the device", func(t *testing.T) {
		got := decodeDevicesBody[map[string]any](t, request(f.rt, http.MethodGet, "/Devices/Info?id=dev-b", "", "", ""), http.StatusOK)
		if got["LastUserName"] != "viewer" || got["AppVersion"] != "1.2" {
			t.Fatalf("dev-b info = %#v, want the viewer row", got)
		}
		assertStatus(t, request(f.rt, http.MethodGet, "/Devices/Info?id=missing", "", "", ""), http.StatusNotFound)
		assertStatus(t, request(f.rt, http.MethodGet, "/Devices/Info", "", "", ""), http.StatusBadRequest)
	})

	t.Run("options keep the custom name", func(t *testing.T) {
		assertStatus(t, request(f.rt, http.MethodGet, "/Devices/Options?id=dev-a", "", "", ""), http.StatusNotFound)
		assertStatus(t, request(f.rt, http.MethodPost, "/Devices/Options?id=dev-a", "", "application/json", ""), http.StatusBadRequest)
		rec := request(f.rt, http.MethodPost, "/Devices/Options?id=dev-a", `{"customName":"Office"}`, "application/json", "")
		assertStatus(t, rec, http.StatusNoContent)
		got := decodeDevicesBody[map[string]any](t, request(f.rt, http.MethodGet, "/Devices/Options?id=dev-a", "", "", ""), http.StatusOK)
		if want := map[string]any{"Id": float64(0), "DeviceId": "dev-a", "CustomName": "Office"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("dev-a options = %#v, want %#v", got, want)
		}
		info := decodeDevicesBody[map[string]any](t, request(f.rt, http.MethodGet, "/Devices/Info?id=dev-a", "", "", ""), http.StatusOK)
		if info["CustomName"] != "Office" {
			t.Fatalf("dev-a CustomName = %v, want Office", info["CustomName"])
		}
		// A null name clears it; the options row stays.
		assertStatus(t, request(f.rt, http.MethodPost, "/Devices/Options?id=dev-a", `{"CustomName":null}`, "application/json", ""), http.StatusNoContent)
		got = decodeDevicesBody[map[string]any](t, request(f.rt, http.MethodGet, "/Devices/Options?id=dev-a", "", "", ""), http.StatusOK)
		if _, present := got["CustomName"]; present {
			t.Fatalf("dev-a options = %#v, want no CustomName", got)
		}
	})

	t.Run("delete logs out every token of the device", func(t *testing.T) {
		f.sessions.RecordAuthenticatedActivity(session.Activity{Client: "Swiftfin", DeviceID: "dev-b", DeviceName: "Apple TV", UserID: f.viewer.ID})
		assertStatus(t, request(f.rt, http.MethodDelete, "/Devices?id=dev-b&id=missing", "", "", ""), http.StatusBadRequest)
		if _, err := f.identity.DeviceByToken(t.Context(), "token-viewer-b"); err != nil {
			t.Fatalf("a rejected delete removed a token: %v", err)
		}
		assertStatus(t, request(f.rt, http.MethodDelete, "/Devices?id=dev-b", "", "", ""), http.StatusNoContent)
		for _, token := range []string{"token-viewer-b", "token-admin-b"} {
			if _, err := f.identity.DeviceByToken(t.Context(), token); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("token %s after delete: %v, want ErrNotFound", token, err)
			}
		}
		if _, ok := f.sessions.Session("Swiftfin", "dev-b"); ok {
			t.Fatal("dev-b session survived the delete")
		}
		if _, err := f.identity.DeviceByToken(t.Context(), "token-admin-a"); err != nil {
			t.Fatalf("delete removed another device: %v", err)
		}
		assertStatus(t, request(f.rt, http.MethodDelete, "/Devices", "", "", ""), http.StatusNoContent)
	})
}

func TestDevicesRequireElevation(t *testing.T) {
	stores, err := sqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("open SQLite: %v", err)
	}
	t.Cleanup(func() { _ = stores.Close() })
	f := newDevicesFixture(t, stores, false)
	for _, call := range []struct{ method, path string }{
		{http.MethodGet, "/Devices"},
		{http.MethodDelete, "/Devices?id=dev-a"},
		{http.MethodGet, "/Devices/Info?id=dev-a"},
		{http.MethodGet, "/Devices/Options?id=dev-a"},
		{http.MethodPost, "/Devices/Options?id=dev-a"},
	} {
		assertStatus(t, request(f.rt, call.method, call.path, `{}`, "application/json", ""), http.StatusForbidden)
	}
	for _, route := range f.rt.Routes() {
		if route.Policy != string(auth.PolicyRequiresElevation) {
			t.Errorf("%s %s policy = %q, want RequiresElevation", route.Method, route.Pattern, route.Policy)
		}
	}
}

func decodeDevicesBody[T any](t *testing.T, rec interface {
	Result() *http.Response
}, status int) T {
	t.Helper()
	response := rec.Result()
	defer response.Body.Close()
	if response.StatusCode != status {
		t.Fatalf("status = %d, want %d", response.StatusCode, status)
	}
	var value T
	if err := json.NewDecoder(response.Body).Decode(&value); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	return value
}

func deviceIDs(items []map[string]any) []string {
	ids := make([]string, 0, len(items))
	for _, entry := range items {
		ids = append(ids, entry["Id"].(string))
	}
	return ids
}

// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
	"github.com/deadfin-org/deadfin/internal/store/storetest"
)

func TestIdentityUsersCRUDPolicyConfigurationAndNormalizedNameUniqueness(t *testing.T) {
	identity := openIdentity(t)
	ctx := t.Context()
	id := mustStoreUUID(t, "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	policy := []byte(`{"IsAdministrator":true,"EnableRemoteAccess":false}`)
	configuration := []byte(`{"SubtitleMode":"Always","PlayDefaultAudioTrack":false}`)

	created, err := identity.CreateUser(ctx, store.User{
		ID:             id,
		Name:           "Ada",
		NormalizedName: "ada",
		Policy:         policy,
		Configuration:  configuration,
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if created.ID != id || created.Name != "Ada" || created.NormalizedName != "ada" {
		t.Fatalf("created user = %#v, want persisted user identity", created)
	}

	got, err := identity.User(ctx, id)
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	assertStoreUserPayload(t, got, policy, configuration)

	got.Policy = []byte(`{"IsAdministrator":false,"EnableRemoteAccess":true}`)
	got.Configuration = []byte(`{"SubtitleMode":"Default","PlayDefaultAudioTrack":true}`)
	if err := identity.UpdateUser(ctx, got); err != nil {
		t.Fatalf("update user policy/configuration: %v", err)
	}
	updated, err := identity.UserByNormalizedName(ctx, "ada")
	if err != nil {
		t.Fatalf("get user by normalized name: %v", err)
	}
	assertStoreUserPayload(t, updated, got.Policy, got.Configuration)

	_, err = identity.CreateUser(ctx, store.User{
		ID:             mustStoreUUID(t, "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"),
		Name:           "ada second account",
		NormalizedName: "ada",
	})
	if !errors.Is(err, store.ErrDuplicateName) {
		t.Fatalf("create duplicate normalized name error = %v, want ErrDuplicateName", err)
	}

	if err := identity.DeleteUser(ctx, id); err != nil {
		t.Fatalf("delete user: %v", err)
	}
	_, err = identity.User(ctx, id)
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("get deleted user error = %v, want ErrNotFound", err)
	}
}

func TestIdentityDeviceTokenLifecycleAndRevokeExceptCurrent(t *testing.T) {
	identity := openIdentity(t)
	ctx := t.Context()
	owner := mustStoreUUID(t, "cccccccc-cccc-cccc-cccc-cccccccccccc")
	other := mustStoreUUID(t, "dddddddd-dddd-dddd-dddd-dddddddddddd")
	for _, user := range []store.User{
		{ID: owner, Name: "owner", NormalizedName: "owner"},
		{ID: other, Name: "other", NormalizedName: "other"},
	} {
		if _, err := identity.CreateUser(ctx, user); err != nil {
			t.Fatalf("create %q: %v", user.Name, err)
		}
	}

	current := createStoreDevice(t, identity, owner, "current-token", "owner-current")
	revoked := createStoreDevice(t, identity, owner, "revoked-token", "owner-other")
	unrelated := createStoreDevice(t, identity, other, "other-user-token", "other-device")

	lookedUp, err := identity.DeviceByToken(ctx, current.AccessToken)
	if err != nil {
		t.Fatalf("lookup issued device token: %v", err)
	}
	if lookedUp.UserID != owner || lookedUp.DeviceID != current.DeviceID || lookedUp.AccessToken != current.AccessToken {
		t.Fatalf("token lookup = %#v, want current device %#v", lookedUp, current)
	}

	if err := identity.RevokeUserTokens(ctx, owner, current.AccessToken); err != nil {
		t.Fatalf("revoke owner tokens except current: %v", err)
	}
	if _, err := identity.DeviceByToken(ctx, current.AccessToken); err != nil {
		t.Fatalf("lookup retained current token: %v", err)
	}
	if _, err := identity.DeviceByToken(ctx, revoked.AccessToken); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("lookup revoked token error = %v, want ErrNotFound", err)
	}
	if _, err := identity.DeviceByToken(ctx, unrelated.AccessToken); err != nil {
		t.Fatalf("lookup unrelated user's token after revoke: %v", err)
	}

	if err := identity.DeleteToken(ctx, current.AccessToken); err != nil {
		t.Fatalf("logout current token: %v", err)
	}
	if _, err := identity.DeviceByToken(ctx, current.AccessToken); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("lookup logged-out token error = %v, want ErrNotFound", err)
	}
}

func TestIdentityUpsertDeviceActivityCreatesThenPreservesOrRefreshesByThreshold(t *testing.T) {
	identity := openIdentity(t)
	ctx := t.Context()
	start := time.Date(2026, time.July, 26, 12, 0, 0, 0, time.UTC)
	owner := mustStoreUUID(t, "eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee")
	if _, err := identity.CreateUser(ctx, store.User{ID: owner, Name: "activity", NormalizedName: "activity"}); err != nil {
		t.Fatalf("create activity user: %v", err)
	}
	device := store.Device{
		UserID:           owner,
		AccessToken:      "activity-token",
		AppName:          "Deadfin Test",
		AppVersion:       "1.0",
		DeviceName:       "Clock Device",
		DeviceID:         "clock-device",
		DateCreated:      start,
		DateModified:     start,
		DateLastActivity: start,
	}

	created, changed, err := identity.UpsertDeviceActivity(ctx, device, 3*time.Minute, start)
	if err != nil {
		t.Fatalf("create device through upsert: %v", err)
	}
	if !changed || created.AccessToken != device.AccessToken || created.DeviceID != device.DeviceID {
		t.Fatalf("initial upsert = (%#v, changed=%t), want created device and changed=true", created, changed)
	}

	below := start.Add(3*time.Minute - time.Nanosecond)
	unchanged, changed, err := identity.UpsertDeviceActivity(ctx, created, 3*time.Minute, below)
	if err != nil {
		t.Fatalf("upsert below activity threshold: %v", err)
	}
	if changed || !unchanged.DateLastActivity.Equal(start) {
		t.Fatalf("below threshold = (%s, changed=%t), want (%s, false)", unchanged.DateLastActivity, changed, start)
	}

	equal := start.Add(3 * time.Minute)
	unchanged, changed, err = identity.UpsertDeviceActivity(ctx, created, 3*time.Minute, equal)
	if err != nil {
		t.Fatalf("upsert at activity threshold: %v", err)
	}
	if changed || !unchanged.DateLastActivity.Equal(start) {
		t.Fatalf("equal threshold = (%s, changed=%t), want (%s, false)", unchanged.DateLastActivity, changed, start)
	}

	above := equal.Add(time.Nanosecond)
	refreshed, changed, err := identity.UpsertDeviceActivity(ctx, created, 3*time.Minute, above)
	if err != nil {
		t.Fatalf("upsert above activity threshold: %v", err)
	}
	if !changed || !refreshed.DateLastActivity.Equal(above) {
		t.Fatalf("above threshold = (%s, changed=%t), want (%s, true)", refreshed.DateLastActivity, changed, above)
	}
}

func TestIdentityActivityRefreshNeverRegressesActivityOrMetadata(t *testing.T) {
	identity := openIdentity(t)
	ctx := t.Context()
	start := time.Date(2026, time.July, 26, 12, 0, 0, 0, time.UTC)
	owner := mustStoreUUID(t, "ffffffff-ffff-ffff-ffff-ffffffffffff")
	if _, err := identity.CreateUser(ctx, store.User{ID: owner, Name: "monotonic", NormalizedName: "MONOTONIC"}); err != nil {
		t.Fatalf("create monotonic user: %v", err)
	}
	created, _, err := identity.UpsertDeviceActivity(ctx, store.Device{
		UserID: owner, AccessToken: "monotonic-token", AppName: "Deadfin Test", AppVersion: "1.0", DeviceName: "original", DeviceID: "monotonic-device",
		DateCreated: start, DateModified: start, DateLastActivity: start,
	}, 3*time.Minute, start)
	if err != nil {
		t.Fatalf("create activity device: %v", err)
	}
	newerNow := start.Add(4 * time.Minute)
	newerInput := created
	newerInput.DeviceName = "newer-name"
	newerInput.AppVersion = "2.0"
	newer, changed, err := identity.UpsertDeviceActivity(ctx, newerInput, 3*time.Minute, newerNow)
	if err != nil || !changed {
		t.Fatalf("apply newer refresh = (%#v, changed=%t, err=%v), want changed newer device", newer, changed, err)
	}
	olderInput := created
	olderInput.DeviceName = "older-name"
	olderInput.AppVersion = "1.5"
	got, changed, err := identity.UpsertDeviceActivity(ctx, olderInput, 3*time.Minute, start.Add(3*time.Minute))
	if err != nil {
		t.Fatalf("apply reverse-ordered refresh: %v", err)
	}
	if changed {
		t.Fatal("reverse-ordered refresh changed device, want no regression")
	}
	if !got.DateLastActivity.Equal(newer.DateLastActivity) || !got.DateModified.Equal(newer.DateModified) || got.DeviceName != newer.DeviceName || got.AppVersion != newer.AppVersion {
		t.Fatalf("reverse-ordered refresh = %#v, want monotonic newer state %#v", got, newer)
	}
}

func TestIdentityGeneratedIDsUsePostgresReturningContract(t *testing.T) {
	db, err := sql.Open("identity-postgres-returning-contract", "")
	if err != nil {
		t.Fatalf("open PostgreSQL contract database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	identity := store.NewIdentity(postgresReturningStores{db: db})
	now := time.Date(2026, time.July, 26, 12, 0, 0, 0, time.UTC)

	device, err := identity.CreateDevice(t.Context(), store.Device{
		UserID: mustStoreUUID(t, "12121212-1212-1212-1212-121212121212"), AccessToken: "returning-device", AppName: "Deadfin Test", AppVersion: "1.0", DeviceName: "Returning", DeviceID: "returning-device",
		DateCreated: now, DateModified: now, DateLastActivity: now,
	})
	if err != nil {
		t.Fatalf("create device using PostgreSQL contract: %v", err)
	}
	if device.ID != 41 {
		t.Fatalf("device generated ID = %d, want 41 returned by INSERT RETURNING", device.ID)
	}
	key, err := identity.PutAPIKey(t.Context(), store.APIKey{Name: "returning-key", AccessToken: "returning-api-key", DateCreated: now, DateLastActivity: now})
	if err != nil {
		t.Fatalf("create API key using PostgreSQL contract: %v", err)
	}
	if key.ID != 41 {
		t.Fatalf("API key generated ID = %d, want 41 returned by INSERT RETURNING", key.ID)
	}
}

func TestIdentityPostgresTokenLifecycleAndMonotonicActivity(t *testing.T) {
	identity := store.NewIdentity(storetest.OpenPostgres(t))
	ctx := t.Context()
	start := time.Date(2026, time.July, 26, 12, 0, 0, 0, time.UTC)
	owner := mustStoreUUID(t, "13131313-1313-1313-1313-131313131313")
	other := mustStoreUUID(t, "14141414-1414-1414-1414-141414141414")
	for _, record := range []store.User{
		{ID: owner, Name: "pg-owner", NormalizedName: "PG-OWNER"},
		{ID: other, Name: "pg-other", NormalizedName: "PG-OTHER"},
	} {
		if _, err := identity.CreateUser(ctx, record); err != nil {
			t.Fatalf("create PostgreSQL user %q: %v", record.Name, err)
		}
	}
	current, err := identity.CreateDevice(ctx, store.Device{UserID: owner, AccessToken: "pg-current", AppName: "Deadfin Test", AppVersion: "1.0", DeviceName: "Current", DeviceID: "pg-current-device", DateCreated: start, DateModified: start, DateLastActivity: start})
	if err != nil {
		t.Fatalf("create PostgreSQL current device: %v", err)
	}
	revoked, err := identity.CreateDevice(ctx, store.Device{UserID: owner, AccessToken: "pg-revoked", AppName: "Deadfin Test", AppVersion: "1.0", DeviceName: "Revoked", DeviceID: "pg-revoked-device", DateCreated: start, DateModified: start, DateLastActivity: start})
	if err != nil {
		t.Fatalf("create PostgreSQL revocable device: %v", err)
	}
	if _, err := identity.CreateDevice(ctx, store.Device{UserID: other, AccessToken: "pg-other", AppName: "Deadfin Test", AppVersion: "1.0", DeviceName: "Other", DeviceID: "pg-other-device", DateCreated: start, DateModified: start, DateLastActivity: start}); err != nil {
		t.Fatalf("create PostgreSQL unrelated device: %v", err)
	}
	if err := identity.RevokeUserTokens(ctx, owner, current.AccessToken); err != nil {
		t.Fatalf("revoke PostgreSQL owner tokens: %v", err)
	}
	if _, err := identity.DeviceByToken(ctx, current.AccessToken); err != nil {
		t.Fatalf("lookup retained PostgreSQL token: %v", err)
	}
	if _, err := identity.DeviceByToken(ctx, revoked.AccessToken); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("lookup revoked PostgreSQL token error = %v, want ErrNotFound", err)
	}
	if _, err := identity.DeviceByToken(ctx, "pg-other"); err != nil {
		t.Fatalf("lookup unrelated PostgreSQL token: %v", err)
	}

	newerInput := current
	newerInput.DeviceName = "Newer"
	newerInput.AppVersion = "2.0"
	newer, changed, err := identity.UpsertDeviceActivity(ctx, newerInput, 3*time.Minute, start.Add(4*time.Minute))
	if err != nil || !changed {
		t.Fatalf("refresh newer PostgreSQL device = (%#v, changed=%t, err=%v), want changed", newer, changed, err)
	}
	olderInput := current
	olderInput.DeviceName = "Older"
	olderInput.AppVersion = "1.5"
	got, changed, err := identity.UpsertDeviceActivity(ctx, olderInput, 3*time.Minute, start.Add(3*time.Minute))
	if err != nil {
		t.Fatalf("refresh stale PostgreSQL device: %v", err)
	}
	if changed || !got.DateModified.Equal(newer.DateModified) || got.DeviceName != newer.DeviceName || got.AppVersion != newer.AppVersion {
		t.Fatalf("stale PostgreSQL refresh = %#v, want unchanged newer state %#v", got, newer)
	}
}

func openIdentity(t *testing.T) *store.Identity {
	t.Helper()
	stores, err := sqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("open SQLite store: %v", err)
	}
	t.Cleanup(func() {
		if err := stores.Close(); err != nil {
			t.Errorf("close SQLite store: %v", err)
		}
	})
	return store.NewIdentity(stores)
}

func mustStoreUUID(t *testing.T, text string) dto.UUID {
	t.Helper()
	id, err := dto.ParseUUID(text)
	if err != nil {
		t.Fatalf("parse UUID %q: %v", text, err)
	}
	return id
}

func createStoreDevice(t *testing.T, identity *store.Identity, userID dto.UUID, token, deviceID string) store.Device {
	t.Helper()
	created, err := identity.CreateDevice(t.Context(), store.Device{
		UserID:      userID,
		AccessToken: token,
		AppName:     "Deadfin Test",
		AppVersion:  "1.0",
		DeviceName:  deviceID,
		DeviceID:    deviceID,
	})
	if err != nil {
		t.Fatalf("create device %q: %v", deviceID, err)
	}
	return created
}

func assertStoreUserPayload(t *testing.T, got store.User, wantPolicy, wantConfiguration []byte) {
	t.Helper()
	if string(got.Policy) != string(wantPolicy) {
		t.Fatalf("policy = %q, want %q", got.Policy, wantPolicy)
	}
	if string(got.Configuration) != string(wantConfiguration) {
		t.Fatalf("configuration = %q, want %q", got.Configuration, wantConfiguration)
	}
}

func init() {
	sql.Register("identity-postgres-returning-contract", postgresReturningDriver{})
}

type postgresReturningStores struct{ db *sql.DB }

func (s postgresReturningStores) Writer() *sql.DB      { return s.db }
func (s postgresReturningStores) Reader() *sql.DB      { return s.db }
func (postgresReturningStores) Dialect() store.Dialect { return postgresReturningDialect{} }
func (s postgresReturningStores) Close() error         { return s.db.Close() }

type postgresReturningDialect struct{}

func (postgresReturningDialect) Placeholder(index int) string { return "$" + strconv.Itoa(index) }
func (postgresReturningDialect) Boolean(value bool) string {
	if value {
		return "TRUE"
	}
	return "FALSE"
}
func (postgresReturningDialect) TimeValue(value time.Time) any   { return value }
func (postgresReturningDialect) ScanTime(any) (time.Time, error) { return time.Time{}, nil }
func (postgresReturningDialect) Random() string                  { return "RANDOM()" }
func (postgresReturningDialect) NullsLast() string               { return "NULLS LAST" }
func (postgresReturningDialect) TextCollation(expression string) string {
	return expression + ` COLLATE "C"`
}
func (postgresReturningDialect) EscapeLike(value string) string { return value }
func (postgresReturningDialect) SearchClause(column, placeholder string) string {
	return column + " = " + placeholder
}
func (postgresReturningDialect) ContainsClause(column, placeholder string) string {
	return column + " = " + placeholder
}

func (postgresReturningDialect) SpanAtMost(start, end string, limit time.Duration) string {
	return end + " - " + start + " <= interval '" + strconv.FormatInt(int64(limit/time.Second), 10) + " seconds'"
}

type postgresReturningDriver struct{}

func (postgresReturningDriver) Open(string) (driver.Conn, error) { return postgresReturningConn{}, nil }

type postgresReturningConn struct{}

func (postgresReturningConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepared statements are not part of the PostgreSQL returning contract")
}
func (postgresReturningConn) Close() error { return nil }
func (postgresReturningConn) Begin() (driver.Tx, error) {
	return nil, errors.New("transactions are not part of the PostgreSQL returning contract")
}

func (postgresReturningConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	return nil, errors.New("generated identity IDs must use INSERT RETURNING, not LastInsertId")
}

func (postgresReturningConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	if !strings.Contains(query, "RETURNING id") || !strings.Contains(query, "$1") {
		return nil, errors.New("PostgreSQL generated-ID query must use $ placeholders and RETURNING id")
	}
	return &postgresReturningRows{}, nil
}

type postgresReturningRows struct{ returned bool }

func (r *postgresReturningRows) Columns() []string { return []string{"id"} }
func (r *postgresReturningRows) Close() error      { return nil }
func (r *postgresReturningRows) Next(dest []driver.Value) error {
	if r.returned {
		return io.EOF
	}
	r.returned = true
	dest[0] = int64(41)
	return nil
}

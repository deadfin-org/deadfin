// SPDX-License-Identifier: GPL-2.0-only

package user_test

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/activitylog"
	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
	"github.com/deadfin-org/deadfin/internal/user"
)

type recordedActivity struct{ entries []activitylog.Entry }

func (r *recordedActivity) Record(entry activitylog.Entry) { r.entries = append(r.entries, entry) }

// TestServiceRecordsUserActivity defends the user activity entries
// (UserCreatedLogger, UserPasswordChangedLogger, UserDeletedLogger,
// UserLockedOutLogger): CreateUserByName with a password also changes the
// password, a password update and a reset each write one entry, a refused
// change writes none, a failed login writes UserLockedOut only when it
// reaches the lockout limit, and a delete writes the deleted name.
func TestServiceRecordsUserActivity(t *testing.T) {
	stores, err := sqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("open SQLite: %v", err)
	}
	t.Cleanup(func() { _ = stores.Close() })
	random := make([]byte, 4096)
	for index := range random {
		random[index] = byte(index * 7)
	}
	activity := &recordedActivity{}
	service := user.New(store.NewIdentity(stores), clock.NewFake(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)), &lockedBytesReader{reader: bytes.NewReader(random)}).WithActivity(activity)
	ctx := t.Context()

	admin, err := service.Create(ctx, "admin", "secret")
	if err != nil {
		t.Fatalf("Create admin: %v", err)
	}
	if err := service.UpdatePolicy(ctx, admin.ID, json.RawMessage(`{"IsAdministrator":true}`)); err != nil {
		t.Fatalf("UpdatePolicy admin: %v", err)
	}
	password := "pw"
	alice, err := service.CreateByName(ctx, "alice", &password)
	if err != nil {
		t.Fatalf("CreateByName alice: %v", err)
	}
	bob, err := service.CreateByName(ctx, "bob", nil)
	if err != nil {
		t.Fatalf("CreateByName bob: %v", err)
	}
	actor := user.Actor{UserID: admin.ID, Administrator: true}
	if err := service.UpdatePassword(ctx, actor, alice.ID, user.PasswordUpdate{New: "new"}); err != nil {
		t.Fatalf("UpdatePassword: %v", err)
	}
	if err := service.UpdatePassword(ctx, actor, alice.ID, user.PasswordUpdate{Reset: true}); err != nil {
		t.Fatalf("UpdatePassword reset: %v", err)
	}
	if err := service.UpdatePassword(ctx, user.Actor{UserID: bob.ID}, alice.ID, user.PasswordUpdate{New: "x"}); err == nil {
		t.Fatal("UpdatePassword by another user succeeded")
	}
	if err := service.UpdatePolicy(ctx, bob.ID, json.RawMessage(`{"LoginAttemptsBeforeLockout":2}`)); err != nil {
		t.Fatalf("UpdatePolicy bob: %v", err)
	}
	if err := service.RecordFailedLogin(ctx, bob.ID); err != nil {
		t.Fatalf("RecordFailedLogin 1: %v", err)
	}
	if err := service.RecordFailedLogin(ctx, bob.ID); err != nil {
		t.Fatalf("RecordFailedLogin 2: %v", err)
	}
	if err := service.Delete(ctx, alice.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	want := []activitylog.Entry{
		activitylog.UserCreated(admin.ID, "admin"),
		activitylog.UserCreated(alice.ID, "alice"),
		activitylog.UserPasswordChanged(alice.ID, "alice"),
		activitylog.UserCreated(bob.ID, "bob"),
		activitylog.UserPasswordChanged(alice.ID, "alice"),
		activitylog.UserPasswordChanged(alice.ID, "alice"),
		activitylog.UserLockedOut(bob.ID, "bob"),
		activitylog.UserDeleted("alice"),
	}
	if len(activity.entries) != len(want) {
		t.Fatalf("entries = %+v; want %d entries", activity.entries, len(want))
	}
	for index, entry := range activity.entries {
		if entry.Name != want[index].Name || entry.Type != want[index].Type || entry.UserID != want[index].UserID || entry.Severity != want[index].Severity {
			t.Errorf("entry %d = %+v; want %+v", index, entry, want[index])
		}
	}
}

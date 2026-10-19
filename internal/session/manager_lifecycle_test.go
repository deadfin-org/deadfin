// SPDX-License-Identifier: GPL-2.0-only

package session_test

import (
	"context"
	"sync"
	"testing"

	"github.com/deadfin-org/deadfin/internal/session"
)

// TestManagerLifecycleHooksRunAfterTheChangeCommits defends the session start
// and end hooks (SessionManager.OnSessionStarted and OnSessionEnded): creation
// by login activity and by authenticated request activity, and removal by
// Remove and EndDeviceSessions, each run the hook once, outside the registry
// lock, so a hook may read the registry.
func TestManagerLifecycleHooksRunAfterTheChangeCommits(t *testing.T) {
	manager, _ := newManager(t)
	var mu sync.Mutex
	var started, ended []session.Snapshot
	if err := manager.SetOptions(session.Options{
		OnSessionStarted: func(_ context.Context, snapshot session.Snapshot) {
			if _, found := manager.SessionByID(snapshot.ID); !found {
				t.Errorf("started session %s is not in the registry", snapshot.ID)
			}
			mu.Lock()
			started = append(started, snapshot)
			mu.Unlock()
		},
		OnSessionEnded: func(_ context.Context, snapshot session.Snapshot) {
			if _, found := manager.SessionByID(snapshot.ID); found {
				t.Errorf("ended session %s is still in the registry", snapshot.ID)
			}
			mu.Lock()
			ended = append(ended, snapshot)
			mu.Unlock()
		},
	}); err != nil {
		t.Fatalf("SetOptions: %v", err)
	}

	web := logActivity(t, manager, "Jellyfin Web", "device-a", sessionTestUserA)
	_ = logActivity(t, manager, "Jellyfin Web", "device-a", sessionTestUserA) // refresh: no hook
	if outcome := manager.RecordAuthenticatedActivity(session.Activity{Client: "Swiftfin", Version: "1", DeviceID: "device-b", DeviceName: "Apple TV", UserID: sessionTestUserB, RemoteEndPoint: "10.0.0.9"}); outcome != session.AuthenticatedActivityCreated {
		t.Fatalf("RecordAuthenticatedActivity = %v; want created", outcome)
	}
	if len(started) != 2 || started[0].ID != web.ID || started[1].DeviceName != "Apple TV" || started[1].RemoteEndPoint != "10.0.0.9" || started[1].UserID != sessionTestUserB {
		t.Fatalf("started = %+v", started)
	}

	if !manager.Remove(web.ID) {
		t.Fatal("Remove reported no session")
	}
	if ended := manager.EndDeviceSessions("DEVICE-B"); len(ended) != 1 {
		t.Fatalf("EndDeviceSessions = %v", ended)
	}
	if len(ended) != 2 || ended[0].ID != web.ID || ended[1].UserID != sessionTestUserB || ended[1].DeviceName != "Apple TV" {
		t.Fatalf("ended = %+v", ended)
	}
}

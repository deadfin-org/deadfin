// SPDX-License-Identifier: GPL-2.0-only

package auth_test

import (
	"net/http"
	"testing"

	"github.com/deadfin-org/deadfin/internal/activitylog"
	"github.com/deadfin-org/deadfin/internal/auth"
)

type recordedActivity struct{ entries []activitylog.Entry }

func (r *recordedActivity) Record(entry activitylog.Entry) { r.entries = append(r.entries, entry) }

// TestLoginRecordsAuthenticationActivity defends the authentication activity
// entries (AuthenticationSucceededLogger, AuthenticationFailedLogger): an
// unknown user name and a wrong password write AuthenticationFailed with the
// requested name, an admission denial writes nothing, and a successful
// password or direct login writes AuthenticationSucceeded for the user.
func TestLoginRecordsAuthenticationActivity(t *testing.T) {
	users, service, _ := openLoginUsers(t, deterministicEntropy())
	target := createLoginUser(t, service, "admission-user")
	activity := &recordedActivity{}
	newRecordingLogin := func(policy auth.LoginPolicy) *auth.Login {
		return auth.NewLogin(auth.LoginConfig{
			Users:    users,
			Policy:   policy,
			Network:  &fakeLoginNetwork{},
			Sessions: &fakeLoginSessions{},
			Server:   loginTestServer,
			Activity: activity,
		})
	}

	unknown := validLoginRequest()
	unknown.Username = "nobody"
	unknown.RemoteEndPoint = "203.0.113.24"
	_, err := newRecordingLogin(&fakeLoginPolicy{}).Authenticate(t.Context(), unknown)
	assertLoginStatus(t, err, http.StatusUnauthorized)
	wrong := validLoginRequest()
	wrong.Password = "incorrect password"
	wrong.RemoteEndPoint = "203.0.113.25"
	_, err = newRecordingLogin(&fakeLoginPolicy{}).Authenticate(t.Context(), wrong)
	assertLoginStatus(t, err, http.StatusUnauthorized)
	_, err = newRecordingLogin(&fakeLoginPolicy{authorizeErr: auth.ErrLoginDenied}).Authenticate(t.Context(), validLoginRequest())
	assertLoginStatus(t, err, http.StatusForbidden)
	success := validLoginRequest()
	success.RemoteEndPoint = "203.0.113.26"
	if _, err := newRecordingLogin(&fakeLoginPolicy{}).Authenticate(t.Context(), success); err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	direct := validDirectLoginRequest(target.ID)
	direct.RemoteEndPoint = "203.0.113.27"
	if _, err := newRecordingLogin(&fakeLoginPolicy{}).AuthenticateDirect(t.Context(), direct); err != nil {
		t.Fatalf("AuthenticateDirect: %v", err)
	}

	want := []activitylog.Entry{
		activitylog.AuthenticationFailed("nobody", "203.0.113.24"),
		activitylog.AuthenticationFailed("admission-user", "203.0.113.25"),
		activitylog.AuthenticationSucceeded(target.ID, "admission-user", "203.0.113.26"),
		activitylog.AuthenticationSucceeded(target.ID, "admission-user", "203.0.113.27"),
	}
	if len(activity.entries) != len(want) {
		t.Fatalf("entries = %+v; want %d", activity.entries, len(want))
	}
	for index, entry := range activity.entries {
		if entry.Name != want[index].Name || entry.Type != want[index].Type || entry.UserID != want[index].UserID || *entry.ShortOverview != *want[index].ShortOverview {
			t.Errorf("entry %d = %+v; want %+v", index, entry, want[index])
		}
	}
}

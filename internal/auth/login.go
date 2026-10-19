// SPDX-License-Identifier: GPL-2.0-only

package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"strings"

	"github.com/deadfin-org/deadfin/internal/activitylog"
	"github.com/deadfin-org/deadfin/internal/config"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/session"
	"github.com/deadfin-org/deadfin/internal/store"
)

var (
	// ErrLoginDenied reports a verified identity denied by an admission policy.
	ErrLoginDenied      = errors.New("login denied")
	errLoginMetadata    = errors.New("login metadata is required")
	errLoginCredentials = errors.New("invalid login credentials")
)

// LoginError describes a login failure in terms handlers can map directly to an
// HTTP response while preserving its underlying cause.
type LoginError struct {
	Status int
	Err    error
}

func (err *LoginError) Error() string {
	if err == nil || err.Err == nil {
		return "login failed"
	}
	return fmt.Sprintf("login failed: %v", err.Err)
}

// Unwrap exposes the underlying login, policy, persistence, or context error.
func (err *LoginError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.Err
}

// LoginUsers supplies identity verification, successful-login persistence, and
// atomic device-token replacement.
type LoginUsers interface {
	GetByName(context.Context, string) (store.User, error)
	VerifyPassword(context.Context, dto.UUID, string) (bool, error)
	RecordSuccessfulLogin(context.Context, dto.UUID) error
	IssueToken(context.Context, store.Device) (store.Device, error)
	Logout(context.Context, string) error
}

// LoginPolicy admits a verified user according to account, network, schedule,
// and device facts owned by the authorization subsystem.
type LoginPolicy interface {
	Authorize(context.Context, store.User, netip.Addr, bool) error
	AuthorizeDevice(context.Context, store.User, string) error
}

// LoginNetwork determines whether a remote address is local to this server.
type LoginNetwork interface {
	IsLocal(netip.Addr) bool
}

// LoginSessions admits and records a successful client/device session.
type LoginSessions interface {
	CheckLimit(context.Context, dto.UUID) error
	LogActivity(context.Context, session.Activity) (session.Snapshot, error)
}

// deviceScopedSessionLimit is an optional refinement for session adapters that
// can distinguish a refresh from a new client/device session without widening
// the stable LoginSessions seam used by existing adapters.
type deviceScopedSessionLimit interface {
	CheckLimitFor(context.Context, dto.UUID, string, string) error
}

// failedLoginRecorder is optional so authentication adapters that do not own
// persisted user policy retain the stable LoginUsers contract.
type failedLoginRecorder interface {
	RecordFailedLogin(context.Context, dto.UUID) error
}

// directUserResolver is an optional refinement for LoginUsers adapters that
// can resolve a user by their dto.UUID. Direct authentication uses it to look
// up the target user; password login does not.
type directUserResolver interface {
	Get(context.Context, dto.UUID) (store.User, error)
}

// LoginConfig supplies the independently owned seams for password login.
type LoginConfig struct {
	Users    LoginUsers
	Policy   LoginPolicy
	Network  LoginNetwork
	Sessions LoginSessions
	Server   config.ServerIdentity
	// LegacyAuthorizationEnabled supplies the current persisted compatibility
	// setting used while extracting anonymous password-login metadata.
	LegacyAuthorizationEnabled func() bool
	// Activity, when set, receives the AuthenticationSucceeded and
	// AuthenticationFailed activity log entries.
	Activity activitylog.Recorder
}

// Login authenticates credentials and creates their corresponding device token
// and active-session record.
type Login struct {
	users                      LoginUsers
	policy                     LoginPolicy
	network                    LoginNetwork
	sessions                   LoginSessions
	server                     config.ServerIdentity
	legacyAuthorizationEnabled func() bool
	activity                   activitylog.Recorder
}

// LoginRequest contains user credentials and the required client/device facts.
type LoginRequest struct {
	Username       string
	Password       string
	Client         string
	DeviceID       string
	Device         string
	Version        string
	RemoteIP       netip.Addr
	RemoteEndPoint string
}

// DirectLoginRequest contains the target user and required client/device facts
// for direct authentication. Direct authentication bypasses password
// verification, admission policy, and failed/successful login recording while
// retaining device access and session limits
// (upstream/jellyfin/Emby.Server.Implementations/Session/SessionManager.cs:1630-1704).
type DirectLoginRequest struct {
	UserID   dto.UUID
	Client   string
	DeviceID string
	Device   string
	Version  string
	// RemoteEndPoint is the client's reported remote host, logged with
	// session activity.
	RemoteEndPoint string
}

// LoginResult is the domain result of a successful password login.
type LoginResult struct {
	User        store.User
	Session     session.Snapshot
	AccessToken string
	ServerID    string
}

// NewLogin constructs a password-login orchestrator from explicit subsystem seams.
func NewLogin(config LoginConfig) *Login {
	return &Login{
		users:                      config.Users,
		policy:                     config.Policy,
		network:                    config.Network,
		sessions:                   config.Sessions,
		server:                     config.Server,
		legacyAuthorizationEnabled: config.LegacyAuthorizationEnabled,
		activity:                   config.Activity,
	}
}

// record queues an activity log entry when the login has a recorder.
func (login *Login) record(entry activitylog.Entry) {
	if login.activity != nil {
		login.activity.Record(entry)
	}
}

// LegacyAuthorizationEnabled reports whether obsolete login metadata carriers
// are accepted. The zero-value configuration is intentionally secure by
// default; production supplies the live persisted setting.
func (login *Login) LegacyAuthorizationEnabled() bool {
	return login != nil && login.legacyAuthorizationEnabled != nil && login.legacyAuthorizationEnabled()
}

// Authenticate validates login metadata, verifies the supplied credentials,
// admits the verified user, atomically replaces the device token, and records
// client session activity.
func (login *Login) Authenticate(ctx context.Context, request LoginRequest) (LoginResult, error) {
	if err := ctx.Err(); err != nil {
		return LoginResult{}, loginFailure(http.StatusInternalServerError, err)
	}
	if !validLoginMetadata(request) {
		return LoginResult{}, loginFailure(http.StatusBadRequest, errLoginMetadata)
	}
	if login == nil || login.users == nil || login.policy == nil || login.network == nil || login.sessions == nil {
		return LoginResult{}, loginFailure(http.StatusInternalServerError, errors.New("login dependencies are required"))
	}

	// An unknown user name and a wrong password write AuthenticationFailed
	// (SessionManager.AuthenticateNewSessionInternal); admission denials do not.
	user, err := login.users.GetByName(ctx, request.Username)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			login.record(activitylog.AuthenticationFailed(request.Username, request.RemoteEndPoint))
			return LoginResult{}, loginFailure(http.StatusUnauthorized, errLoginCredentials)
		}
		return LoginResult{}, loginFailure(http.StatusInternalServerError, err)
	}
	verified, err := login.users.VerifyPassword(ctx, user.ID, request.Password)
	if err != nil {
		return LoginResult{}, loginFailure(http.StatusInternalServerError, err)
	}
	if !verified {
		if recorder, ok := login.users.(failedLoginRecorder); ok {
			if err := recorder.RecordFailedLogin(ctx, user.ID); err != nil {
				return LoginResult{}, loginFailure(http.StatusInternalServerError, err)
			}
		}
		login.record(activitylog.AuthenticationFailed(request.Username, request.RemoteEndPoint))
		return LoginResult{}, loginFailure(http.StatusUnauthorized, errLoginCredentials)
	}

	local := !request.RemoteIP.IsValid() || login.network.IsLocal(request.RemoteIP)
	if err := login.policy.Authorize(ctx, user, request.RemoteIP, local); err != nil {
		return LoginResult{}, loginAdmissionFailure(err)
	}
	if err := login.policy.AuthorizeDevice(ctx, user, request.DeviceID); err != nil {
		return LoginResult{}, loginAdmissionFailure(err)
	}
	if err := login.users.RecordSuccessfulLogin(ctx, user.ID); err != nil {
		return LoginResult{}, loginFailure(http.StatusInternalServerError, err)
	}
	return login.issueTokenAndLogActivity(ctx, user, request.Client, request.Device, request.DeviceID, request.Version, request.RemoteEndPoint)
}

// AuthenticateDirect validates the same four client/device metadata fields as
// password login, resolves the target user by dto.UUID through the optional
// directUserResolver refinement on LoginUsers, applies AuthorizeDevice, then
// applies the existing device-scoped session limit before issuing the token
// and logging activity through the shared helper.
//
// Deliberately does not call VerifyPassword, RecordFailedLogin,
// RecordSuccessfulLogin, LoginPolicy.Authorize, or the
// network/schedule/disabled-account admission path: pinned AuthenticateDirect
// bypasses password admission while retaining device access and session
// limits (SessionManager.cs:1630-1704).
func (login *Login) AuthenticateDirect(ctx context.Context, request DirectLoginRequest) (LoginResult, error) {
	if err := ctx.Err(); err != nil {
		return LoginResult{}, loginFailure(http.StatusInternalServerError, err)
	}
	if !validDirectLoginMetadata(request) {
		return LoginResult{}, loginFailure(http.StatusBadRequest, errLoginMetadata)
	}
	if login == nil || login.users == nil || login.policy == nil || login.sessions == nil {
		return LoginResult{}, loginFailure(http.StatusInternalServerError, errors.New("login dependencies are required"))
	}
	resolver, ok := login.users.(directUserResolver)
	if !ok {
		return LoginResult{}, loginFailure(http.StatusInternalServerError, errors.New("direct authentication requires user resolution"))
	}
	user, err := resolver.Get(ctx, request.UserID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return LoginResult{}, loginFailure(http.StatusNotFound, err)
		}
		return LoginResult{}, loginFailure(http.StatusInternalServerError, err)
	}
	if err := login.policy.AuthorizeDevice(ctx, user, request.DeviceID); err != nil {
		return LoginResult{}, loginAdmissionFailure(err)
	}
	return login.issueTokenAndLogActivity(ctx, user, request.Client, request.Device, request.DeviceID, request.Version, request.RemoteEndPoint)
}

// issueTokenAndLogActivity is the shared token-issuance, session-activity, and
// rollback tail used by both password and direct authentication. It applies
// the device-scoped session limit (when available), issues the device token,
// and logs session activity. If activity logging fails, the new token is
// revoked with Logout(context.WithoutCancel(ctx), token).
func (login *Login) issueTokenAndLogActivity(ctx context.Context, user store.User, client, deviceName, deviceID, version, remoteEndPoint string) (LoginResult, error) {
	var err error
	if scoped, ok := login.sessions.(deviceScopedSessionLimit); ok {
		err = scoped.CheckLimitFor(ctx, user.ID, client, deviceID)
	} else {
		err = login.sessions.CheckLimit(ctx, user.ID)
	}
	if err != nil {
		return LoginResult{}, loginAdmissionFailure(err)
	}

	device, err := login.users.IssueToken(ctx, store.Device{
		UserID:     user.ID,
		AppName:    client,
		AppVersion: version,
		DeviceName: deviceName,
		DeviceID:   deviceID,
	})
	if err != nil {
		return LoginResult{}, loginFailure(http.StatusInternalServerError, err)
	}
	snapshot, err := login.sessions.LogActivity(ctx, session.Activity{
		Client:         client,
		Version:        version,
		DeviceID:       deviceID,
		DeviceName:     deviceName,
		UserID:         user.ID,
		RemoteEndPoint: remoteEndPoint,
	})
	if err != nil {
		if logoutErr := login.users.Logout(context.WithoutCancel(ctx), device.AccessToken); logoutErr != nil {
			err = errors.Join(err, fmt.Errorf("revoke login token after session activity failure: %w", logoutErr))
		}
		return LoginResult{}, loginFailure(http.StatusInternalServerError, err)
	}
	login.record(activitylog.AuthenticationSucceeded(user.ID, user.Name, remoteEndPoint))
	return LoginResult{
		User:        user,
		Session:     snapshot,
		AccessToken: device.AccessToken,
		ServerID:    login.server.ID.String(),
	}, nil
}

func validLoginMetadata(request LoginRequest) bool {
	return strings.TrimSpace(request.Client) != "" &&
		strings.TrimSpace(request.DeviceID) != "" &&
		strings.TrimSpace(request.Device) != "" &&
		strings.TrimSpace(request.Version) != ""
}

func validDirectLoginMetadata(request DirectLoginRequest) bool {
	return strings.TrimSpace(request.Client) != "" &&
		strings.TrimSpace(request.DeviceID) != "" &&
		strings.TrimSpace(request.Device) != "" &&
		strings.TrimSpace(request.Version) != ""
}

func loginAdmissionFailure(err error) error {
	if errors.Is(err, ErrLoginDenied) || errors.Is(err, session.ErrLimit) {
		return loginFailure(http.StatusForbidden, err)
	}
	return loginFailure(http.StatusInternalServerError, err)
}

func loginFailure(status int, err error) error {
	return &LoginError{Status: status, Err: err}
}

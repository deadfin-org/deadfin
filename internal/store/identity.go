// SPDX-License-Identifier: GPL-2.0-only

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/deadfin-org/deadfin/internal/dto"
)

var (
	// ErrNotFound reports that a requested store record does not exist.
	ErrNotFound = fmt.Errorf("%w: identity record not found", sql.ErrNoRows)
	// ErrDuplicateName reports a normalized user-name uniqueness violation.
	ErrDuplicateName = errors.New("duplicate normalized user name")
	// ErrStartupUserConfigured reports an attempt to reconfigure the first user.
	ErrStartupUserConfigured = errors.New("startup user is already configured")
)

// User is the persisted identity record. Policy and Configuration are opaque JSON
// documents: granular upstream policy/preference rows are deliberately flattened
// until a query requires an independently stored fact.
type User struct {
	ID               dto.UUID
	Name             string
	NormalizedName   string
	PasswordHash     string
	Policy           []byte
	Configuration    []byte
	LastLoginDate    *time.Time
	LastActivityDate *time.Time
}

// FirstUserUpdate atomically applies the startup wizard's optional rename and
// required password to the first persisted user. Rename distinguishes an
// omitted name from an explicit empty name.
type FirstUserUpdate struct {
	Name           string
	NormalizedName string
	Rename         bool
	PasswordHash   string
}

// Device is Jellyfin's persisted authorization token. One access token owns one
// device row; there is no token table mirroring this fact.
type Device struct {
	ID               int64
	UserID           dto.UUID
	AccessToken      string
	AppName          string
	AppVersion       string
	DeviceName       string
	DeviceID         string
	DateCreated      time.Time
	DateModified     time.Time
	DateLastActivity time.Time
}

type DeviceOptions struct {
	DeviceID   string
	CustomName *string
}

// DeviceRecord is a device row with the facts that DeviceInfoDto joins to it:
// the owner's name and the custom name from the device options.
type DeviceRecord struct {
	Device
	UserName   string
	CustomName *string
}

type APIKey struct {
	ID               int64
	Name             string
	AccessToken      string
	DateCreated      time.Time
	DateLastActivity time.Time
}

type DisplayPreference struct {
	UserID dto.UUID
	ItemID dto.UUID
	Client string
	Data   []byte
}

type CustomItemDisplayPreference struct {
	UserID dto.UUID
	ItemID dto.UUID
	Client string
	Key    string
	Value  *string
}

type AccessSchedule struct {
	ID        int64
	UserID    dto.UUID
	DayOfWeek int
	StartHour float64
	EndHour   float64
}

// Identity is the typed persistence adapter for user and device security data.
// It keeps SQL in internal/store while callers use typed methods.
type Identity struct{ stores Stores }

func NewIdentity(stores Stores) *Identity { return &Identity{stores: stores} }

const identityMutationLock = 0x4465616466696e

func (s *Identity) identityMutationTx(ctx context.Context) (*sql.Tx, error) {
	postgres := s.p(1) == "$1"
	isolation := sql.LevelSerializable
	if postgres {
		// The transaction advisory lock serializes all identity writers. Read
		// committed makes the statement after a waited-for lock observe the
		// winner's commit instead of retaining a stale serializable snapshot.
		isolation = sql.LevelReadCommitted
	}
	tx, err := s.stores.Writer().BeginTx(ctx, &sql.TxOptions{Isolation: isolation})
	if err != nil {
		return nil, err
	}
	if postgres {
		if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock("+fmt.Sprintf("%d", identityMutationLock)+")"); err != nil {
			_ = tx.Rollback()
			return nil, err
		}
	}
	return tx, nil
}

func (s *Identity) forUpdate() string {
	if s.p(1) == "$1" {
		return " FOR UPDATE"
	}
	return ""
}

// CreateUser persists one user after applying the common creation defaults.
func (s *Identity) CreateUser(ctx context.Context, user User) (created User, err error) {
	user = userForCreate(user)
	tx, err := s.identityMutationTx(ctx)
	if err != nil {
		return User{}, err
	}
	defer rollbackOnReturn(tx, &err)
	if err := s.insertUser(ctx, tx, user); err != nil {
		return User{}, err
	}
	if err := tx.Commit(); err != nil {
		return User{}, err
	}
	return user, nil
}

// CreateUserIfEmpty inserts user only when the user set is empty. The empty
// check and insert share one serializable writer transaction, so concurrent
// startup callers can admit at most one first user across supported dialects.
func (s *Identity) CreateUserIfEmpty(ctx context.Context, user User) (created bool, err error) {
	user = userForCreate(user)
	tx, err := s.identityMutationTx(ctx)
	if err != nil {
		return false, err
	}
	defer rollbackOnReturn(tx, &err)
	created, err = s.insertUserIfEmpty(ctx, tx, user)
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return created, nil
}

func userForCreate(user User) User {
	if user.Policy == nil {
		user.Policy = []byte("{}")
	}
	if user.Configuration == nil {
		user.Configuration = []byte("{}")
	}
	return user
}

func (s *Identity) insertUser(ctx context.Context, tx *sql.Tx, user User) error {
	_, err := tx.ExecContext(ctx, "INSERT INTO users (id, name, normalized_name, password_hash, policy, configuration, last_login_date, last_activity_date) VALUES ("+s.p(1)+", "+s.p(2)+", "+s.p(3)+", "+s.p(4)+", "+s.p(5)+", "+s.p(6)+", "+s.p(7)+", "+s.p(8)+")", s.idValue(user.ID), user.Name, user.NormalizedName, nullableString(user.PasswordHash), user.Policy, user.Configuration, timeArg(s.stores.Dialect(), user.LastLoginDate), timeArg(s.stores.Dialect(), user.LastActivityDate))
	if err != nil {
		return userWriteError(err)
	}
	return nil
}

func (s *Identity) insertUserIfEmpty(ctx context.Context, tx *sql.Tx, user User) (bool, error) {
	result, err := tx.ExecContext(ctx, "INSERT INTO users (id, name, normalized_name, password_hash, policy, configuration, last_login_date, last_activity_date) SELECT "+s.p(1)+", "+s.p(2)+", "+s.p(3)+", "+s.p(4)+", "+s.p(5)+", "+s.p(6)+", "+s.p(7)+", "+s.p(8)+" WHERE NOT EXISTS (SELECT 1 FROM users)", s.idValue(user.ID), user.Name, user.NormalizedName, nullableString(user.PasswordHash), user.Policy, user.Configuration, timeArg(s.stores.Dialect(), user.LastLoginDate), timeArg(s.stores.Dialect(), user.LastActivityDate))
	if err != nil {
		return false, userWriteError(err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return count == 1, nil
}

func (s *Identity) User(ctx context.Context, id dto.UUID) (User, error) {
	return s.user(ctx, "SELECT id, name, normalized_name, password_hash, policy, configuration, last_login_date, last_activity_date FROM users WHERE id = "+s.p(1), s.idValue(id))
}

func (s *Identity) UserByNormalizedName(ctx context.Context, name string) (User, error) {
	return s.user(ctx, "SELECT id, name, normalized_name, password_hash, policy, configuration, last_login_date, last_activity_date FROM users WHERE normalized_name = "+s.p(1), name)
}

// FirstUser returns the deterministic startup user without exposing a list
// traversal to callers that need exactly the initial account.
func (s *Identity) FirstUser(ctx context.Context) (User, error) {
	return s.user(ctx, "SELECT id, name, normalized_name, password_hash, policy, configuration, last_login_date, last_activity_date FROM users ORDER BY id LIMIT 1")
}

// ConfigureFirstUser applies the startup wizard's optional rename and password
// as one identity mutation. The SQLite writer connection opens its
// transactions with BEGIN IMMEDIATE, so a concurrent writer that cannot
// acquire the writer lock at begin returns SQLITE_BUSY. This loop retries
// that transient error a bounded number of times. Each retry starts a fresh
// transaction which observes the committed winner.
func (s *Identity) ConfigureFirstUser(ctx context.Context, update FirstUserUpdate) error {
	const retries = 3
	for attempt := range retries {
		err := s.configureFirstUser(ctx, update)
		if !s.sqliteBusy(err) || attempt == retries-1 {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(attempt+1) * time.Millisecond):
		}
	}
	return nil
}

func (s *Identity) configureFirstUser(ctx context.Context, update FirstUserUpdate) (err error) {
	tx, err := s.identityMutationTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackOnReturn(tx, &err)

	first, err := s.scanUser(tx.QueryRowContext(ctx, "SELECT id, name, normalized_name, password_hash, policy, configuration, last_login_date, last_activity_date FROM users ORDER BY id LIMIT 1"+s.forUpdate()))
	if err != nil {
		return err
	}
	if first.PasswordHash != "" {
		return ErrStartupUserConfigured
	}
	if update.Rename && !strings.EqualFold(first.Name, update.Name) {
		first.Name = update.Name
		first.NormalizedName = update.NormalizedName
	}
	result, err := tx.ExecContext(ctx, "UPDATE users SET name = "+s.p(1)+", normalized_name = "+s.p(2)+", password_hash = "+s.p(3)+" WHERE id = "+s.p(4)+" AND (password_hash IS NULL OR password_hash = '')", first.Name, first.NormalizedName, nullableString(update.PasswordHash), s.idValue(first.ID))
	if err != nil {
		return userWriteError(err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrStartupUserConfigured
	}
	return tx.Commit()
}

func (s *Identity) sqliteBusy(err error) bool {
	if err == nil || s.p(1) == "$1" {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "sqlite_busy") || strings.Contains(message, "database is locked") || strings.Contains(message, "database is busy")
}

func (s *Identity) user(ctx context.Context, query string, args ...any) (User, error) {
	return s.scanUser(s.stores.Reader().QueryRowContext(ctx, query, args...))
}

type identityScanner interface{ Scan(...any) error }

func (s *Identity) scanUser(row identityScanner) (User, error) {
	var user User
	var id, lastLogin, lastActivity any
	var password sql.NullString
	if err := row.Scan(&id, &user.Name, &user.NormalizedName, &password, &user.Policy, &user.Configuration, &lastLogin, &lastActivity); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return User{}, ErrNotFound
		}
		return User{}, err
	}
	if err := scanIdentityUUID(&user.ID, id); err != nil {
		return User{}, err
	}
	if !isNil(lastLogin) {
		value, err := s.stores.Dialect().ScanTime(lastLogin)
		if err != nil {
			return User{}, err
		}
		user.LastLoginDate = &value
	}
	if !isNil(lastActivity) {
		value, err := s.stores.Dialect().ScanTime(lastActivity)
		if err != nil {
			return User{}, err
		}
		user.LastActivityDate = &value
	}
	user.PasswordHash = password.String
	return user, nil
}

func (s *Identity) UpdateUser(ctx context.Context, user User) (err error) {
	tx, err := s.identityMutationTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackOnReturn(tx, &err)
	result, err := tx.ExecContext(ctx, "UPDATE users SET name = "+s.p(1)+", normalized_name = "+s.p(2)+", policy = "+s.p(3)+", configuration = "+s.p(4)+" WHERE id = "+s.p(5), user.Name, user.NormalizedName, user.Policy, user.Configuration, s.idValue(user.ID))
	if err != nil {
		return userWriteError(err)
	}
	if err := changed(result); err != nil {
		return err
	}
	return tx.Commit()
}

// RecordSuccessfulLogin persists the user-level activity and login timestamps
// updated by upstream after an admitted password authentication. A successful
// login also clears the persisted failed-attempt count; the caller still holds
// the pre-reset user snapshot for the response produced by that authentication.
func (s *Identity) RecordSuccessfulLogin(ctx context.Context, id dto.UUID, at time.Time) error {
	_, err := s.recordLoginAttempt(ctx, id, at, true)
	return err
}

// RecordFailedLogin increments the persisted failed-attempt count and locks an
// account when its configured limit is reached. It reports whether this
// attempt reached the limit and disabled the account (UserManager
// IncrementInvalidLoginAttemptCount publishes UserLockedOutEventArgs then).
func (s *Identity) RecordFailedLogin(ctx context.Context, id dto.UUID) (lockedOut bool, err error) {
	return s.recordLoginAttempt(ctx, id, time.Time{}, false)
}

func (s *Identity) recordLoginAttempt(ctx context.Context, id dto.UUID, at time.Time, successful bool) (lockedOut bool, err error) {
	tx, err := s.identityMutationTx(ctx)
	if err != nil {
		return false, err
	}
	defer rollbackOnReturn(tx, &err)

	var raw []byte
	if err := tx.QueryRowContext(ctx, "SELECT policy FROM users WHERE id = "+s.p(1)+s.forUpdate(), s.idValue(id)).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, ErrNotFound
		}
		return false, userWriteError(err)
	}
	var policy map[string]json.RawMessage
	if err := json.Unmarshal(raw, &policy); err != nil {
		return false, fmt.Errorf("decode user policy: %w", err)
	}
	if policy == nil {
		policy = make(map[string]json.RawMessage)
	}

	attempts := policyInt(policy, "InvalidLoginAttemptCount", 0)
	if successful {
		policy["InvalidLoginAttemptCount"] = json.RawMessage("0")
	} else {
		attempts++
		policy["InvalidLoginAttemptCount"] = json.RawMessage(strconv.Itoa(attempts))
		limit := policyInt(policy, "LoginAttemptsBeforeLockout", -1)
		if limit == 0 {
			limit = 3
		}
		if limit > 0 && attempts >= limit {
			policy["IsDisabled"] = json.RawMessage("true")
			lockedOut = true
		}
	}
	encoded, err := json.Marshal(policy)
	if err != nil {
		return false, fmt.Errorf("encode user policy: %w", err)
	}
	query := "UPDATE users SET policy = " + s.p(1)
	args := []any{encoded}
	if successful {
		query += ", last_login_date = " + s.p(2) + ", last_activity_date = " + s.p(3) + " WHERE id = " + s.p(4)
		stamp := s.stores.Dialect().TimeValue(at.UTC())
		args = append(args, stamp, stamp, s.idValue(id))
	} else {
		query += " WHERE id = " + s.p(2)
		args = append(args, s.idValue(id))
	}
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return false, userWriteError(err)
	}
	if err := changed(result); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return lockedOut, nil
}

func policyInt(policy map[string]json.RawMessage, key string, fallback int) int {
	raw, found := policy[key]
	if !found || json.Unmarshal(raw, &fallback) != nil {
		return fallback
	}
	return fallback
}

// DeleteUserIf evaluates a caller-supplied pure policy guard against a stable
// writer transaction before deleting. It keeps cardinality safeguards atomic.
func (s *Identity) DeleteUserIf(ctx context.Context, id dto.UUID, guard func([]User, User) error) (err error) {
	tx, err := s.identityMutationTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackOnReturn(tx, &err)
	users, err := s.listUsers(ctx, tx)
	if err != nil {
		return err
	}
	var target User
	found := false
	for _, user := range users {
		if user.ID == id {
			target, found = user, true
			break
		}
	}
	if !found {
		return ErrNotFound
	}
	if err := guard(users, target); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, "DELETE FROM users WHERE id = "+s.p(1), s.idValue(id))
	if err != nil {
		return err
	}
	if err := changed(result); err != nil {
		return err
	}
	return tx.Commit()
}

// UpdateUserIf evaluates policy-sensitive updates in the same writer
// transaction as their replacement, preventing concurrent last-admin demotion.
func (s *Identity) UpdateUserIf(ctx context.Context, user User, guard func(User, []User) error) (err error) {
	tx, err := s.identityMutationTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackOnReturn(tx, &err)
	users, err := s.listUsers(ctx, tx)
	if err != nil {
		return err
	}
	var old User
	found := false
	for _, candidate := range users {
		if candidate.ID == user.ID {
			old, found = candidate, true
			break
		}
	}
	if !found {
		return ErrNotFound
	}
	if err := guard(old, users); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, "UPDATE users SET name = "+s.p(1)+", normalized_name = "+s.p(2)+", policy = "+s.p(3)+", configuration = "+s.p(4)+" WHERE id = "+s.p(5), user.Name, user.NormalizedName, user.Policy, user.Configuration, s.idValue(user.ID))
	if err != nil {
		return userWriteError(err)
	}
	if err := changed(result); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Identity) listUsers(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
},
) ([]User, error) {
	rows, err := q.QueryContext(ctx, "SELECT id, name, normalized_name, password_hash, policy, configuration, last_login_date, last_activity_date FROM users ORDER BY id")
	if err != nil {
		return nil, err
	}
	var users []User
	for rows.Next() {
		user, err := s.scanUser(rows)
		if err != nil {
			return nil, closeRows(rows, err)
		}
		users = append(users, user)
	}
	return users, closeRows(rows, rows.Err())
}

func (s *Identity) DeleteUser(ctx context.Context, id dto.UUID) (err error) {
	tx, err := s.identityMutationTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackOnReturn(tx, &err)
	result, err := tx.ExecContext(ctx, "DELETE FROM users WHERE id = "+s.p(1), s.idValue(id))
	if err != nil {
		return err
	}
	if err := changed(result); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Identity) SetPasswordHash(ctx context.Context, id dto.UUID, passwordHash string) error {
	return s.SetPasswordHashIf(ctx, id, passwordHash, func(User) error { return nil })
}

// SetPasswordHashIf evaluates an account-policy guard and changes the hash in
// one serializable transaction, closing promotion-versus-password-clear races.
func (s *Identity) SetPasswordHashIf(ctx context.Context, id dto.UUID, passwordHash string, guard func(User) error) (err error) {
	tx, err := s.identityMutationTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackOnReturn(tx, &err)
	user, err := s.scanUser(tx.QueryRowContext(ctx, "SELECT id, name, normalized_name, password_hash, policy, configuration, last_login_date, last_activity_date FROM users WHERE id = "+s.p(1), s.idValue(id)))
	if err != nil {
		return err
	}
	if err := guard(user); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, "UPDATE users SET password_hash = "+s.p(1)+" WHERE id = "+s.p(2), nullableString(passwordHash), s.idValue(id))
	if err != nil {
		return err
	}
	if err := changed(result); err != nil {
		return err
	}
	return tx.Commit()
}

// ReplaceUserConfigurationIf replaces one user's opaque configuration after a
// caller-supplied guard has observed the same locked identity row.
func (s *Identity) ReplaceUserConfigurationIf(ctx context.Context, id dto.UUID, configuration []byte, guard func(User) error) (err error) {
	tx, err := s.identityMutationTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackOnReturn(tx, &err)
	user, err := s.scanUser(tx.QueryRowContext(ctx, "SELECT id, name, normalized_name, password_hash, policy, configuration, last_login_date, last_activity_date FROM users WHERE id = "+s.p(1), s.idValue(id)))
	if err != nil {
		return err
	}
	if err := guard(user); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, "UPDATE users SET configuration = "+s.p(1)+" WHERE id = "+s.p(2), configuration, s.idValue(id))
	if err != nil {
		return err
	}
	if err := changed(result); err != nil {
		return err
	}
	return tx.Commit()
}

// SetPasswordHashAndRevokeTokensIf changes a credential and removes every
// other device token in one identity transaction. The current token comparison
// is case-insensitive to match the existing revocation behavior.
func (s *Identity) SetPasswordHashAndRevokeTokensIf(ctx context.Context, id dto.UUID, passwordHash, currentToken string, guard func(User) error) (err error) {
	tx, err := s.identityMutationTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackOnReturn(tx, &err)
	user, err := s.scanUser(tx.QueryRowContext(ctx, "SELECT id, name, normalized_name, password_hash, policy, configuration, last_login_date, last_activity_date FROM users WHERE id = "+s.p(1), s.idValue(id)))
	if err != nil {
		return err
	}
	if err := guard(user); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, "UPDATE users SET password_hash = "+s.p(1)+" WHERE id = "+s.p(2), nullableString(passwordHash), s.idValue(id))
	if err != nil {
		return err
	}
	if err := changed(result); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM devices WHERE user_id = "+s.p(1)+" AND LOWER(access_token) <> LOWER("+s.p(2)+")", s.idValue(id), currentToken); err != nil {
		return err
	}
	return tx.Commit()
}

// CompareAndSwapPasswordHash changes a password only when it still matches the
// verified value. It prevents a legacy-login rehash from overwriting a concurrent
// explicit password change.
func (s *Identity) CompareAndSwapPasswordHash(ctx context.Context, id dto.UUID, oldHash, newHash string) (swapped bool, err error) {
	tx, err := s.identityMutationTx(ctx)
	if err != nil {
		return false, err
	}
	defer rollbackOnReturn(tx, &err)
	result, err := tx.ExecContext(ctx, "UPDATE users SET password_hash = "+s.p(1)+" WHERE id = "+s.p(2)+" AND password_hash = "+s.p(3), nullableString(newHash), s.idValue(id), nullableString(oldHash))
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return count == 1, nil
}

// ListUsers provides the complete policy set to the user service while keeping
// SQL and policy documents in the typed store seam.
func (s *Identity) ListUsers(ctx context.Context) ([]User, error) {
	return s.listUsers(ctx, s.stores.Reader())
}

func (s *Identity) CreateDevice(ctx context.Context, device Device) (Device, error) {
	return s.insertDevice(ctx, s.stores.Writer(), device)
}

// ReplaceDeviceToken atomically replaces every token for a user/device pair.
// If insertion fails (including a duplicate generated token), rollback retains
// the previously valid device row.
func (s *Identity) ReplaceDeviceToken(ctx context.Context, device Device) (replacement Device, err error) {
	tx, err := s.identityMutationTx(ctx)
	if err != nil {
		return Device{}, err
	}
	defer rollbackOnReturn(tx, &err)
	if _, err := tx.ExecContext(ctx, "DELETE FROM devices WHERE user_id = "+s.p(1)+" AND device_id = "+s.p(2), s.idValue(device.UserID), device.DeviceID); err != nil {
		return Device{}, err
	}
	created, err := s.insertDevice(ctx, tx, device)
	if err != nil {
		return Device{}, err
	}
	if err := tx.Commit(); err != nil {
		return Device{}, err
	}
	return created, nil
}

type rowQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (s *Identity) insertDevice(ctx context.Context, q rowQuerier, device Device) (Device, error) {
	err := q.QueryRowContext(ctx, "INSERT INTO devices (user_id, access_token, app_name, app_version, device_name, device_id, date_created, date_modified, date_last_activity) VALUES ("+s.p(1)+", "+s.p(2)+", "+s.p(3)+", "+s.p(4)+", "+s.p(5)+", "+s.p(6)+", "+s.p(7)+", "+s.p(8)+", "+s.p(9)+") RETURNING id", s.idValue(device.UserID), device.AccessToken, device.AppName, device.AppVersion, device.DeviceName, device.DeviceID, s.timeValue(device.DateCreated), s.timeValue(device.DateModified), s.timeValue(device.DateLastActivity)).Scan(&device.ID)
	if err != nil {
		return Device{}, err
	}
	return device, nil
}

func (s *Identity) DeviceByToken(ctx context.Context, token string) (Device, error) {
	return s.device(ctx, "SELECT id, user_id, access_token, app_name, app_version, device_name, device_id, date_created, date_modified, date_last_activity FROM devices WHERE access_token = "+s.p(1), token)
}

func (s *Identity) device(ctx context.Context, query string, args ...any) (Device, error) {
	return s.scanDevice(s.stores.Reader().QueryRowContext(ctx, query, args...))
}

// ListDeviceRecords returns the device rows, with the owner's name and the
// custom name, in id order. A non-empty deviceID selects the rows of that
// device only.
func (s *Identity) ListDeviceRecords(ctx context.Context, deviceID string) ([]DeviceRecord, error) {
	query := "SELECT d.id, d.user_id, d.access_token, d.app_name, d.app_version, d.device_name, d.device_id, d.date_created, d.date_modified, d.date_last_activity, u.name, o.custom_name FROM devices d JOIN users u ON u.id = d.user_id LEFT JOIN device_options o ON o.device_id = d.device_id"
	var args []any
	if deviceID != "" {
		query += " WHERE d.device_id = " + s.p(1)
		args = append(args, deviceID)
	}
	query += " ORDER BY d.id"
	rows, err := s.stores.Reader().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := []DeviceRecord{}
	for rows.Next() {
		var record DeviceRecord
		var customName sql.NullString
		if record.Device, err = s.scanDevice(rows, &record.UserName, &customName); err != nil {
			return nil, err
		}
		if customName.Valid {
			record.CustomName = &customName.String
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

// scanDevice scans the ten device columns and then any extra columns.
func (s *Identity) scanDevice(row identityScanner, extra ...any) (Device, error) {
	var device Device
	var userID any
	var created, modified, activity any
	if err := row.Scan(append([]any{&device.ID, &userID, &device.AccessToken, &device.AppName, &device.AppVersion, &device.DeviceName, &device.DeviceID, &created, &modified, &activity}, extra...)...); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Device{}, ErrNotFound
		}
		return Device{}, err
	}
	if err := scanIdentityUUID(&device.UserID, userID); err != nil {
		return Device{}, err
	}
	var err error
	if device.DateCreated, err = s.stores.Dialect().ScanTime(created); err != nil {
		return Device{}, err
	}
	if device.DateModified, err = s.stores.Dialect().ScanTime(modified); err != nil {
		return Device{}, err
	}
	if device.DateLastActivity, err = s.stores.Dialect().ScanTime(activity); err != nil {
		return Device{}, err
	}
	return device, nil
}

func (s *Identity) DeleteToken(ctx context.Context, token string) error {
	result, err := s.stores.Writer().ExecContext(ctx, "DELETE FROM devices WHERE access_token = "+s.p(1), token)
	if err != nil {
		return err
	}
	return changed(result)
}

// DeleteUserDeviceTokens removes a user's existing tokens for one physical
// device before SessionManager-compatible token issuance.
func (s *Identity) DeleteUserDeviceTokens(ctx context.Context, userID dto.UUID, deviceID string) error {
	_, err := s.stores.Writer().ExecContext(ctx, "DELETE FROM devices WHERE user_id = "+s.p(1)+" AND device_id = "+s.p(2), s.idValue(userID), deviceID)
	return err
}

// ClearUserVolatileState removes user-scoped session and preference state that
// does not belong to the durable account record. It is deliberately idempotent
// so callers can converge an account after an interrupted prior operation.
func (s *Identity) ClearUserVolatileState(ctx context.Context, userID dto.UUID) (err error) {
	tx, err := s.identityMutationTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackOnReturn(tx, &err)
	for _, query := range []string{
		"DELETE FROM device_options WHERE device_id IN (SELECT device_id FROM devices WHERE user_id = " + s.p(1) + ")",
		"DELETE FROM devices WHERE user_id = " + s.p(1),
		"DELETE FROM display_preferences WHERE user_id = " + s.p(1),
		"DELETE FROM custom_item_display_preferences WHERE user_id = " + s.p(1),
		"DELETE FROM access_schedules WHERE user_id = " + s.p(1),
	} {
		if _, err = tx.ExecContext(ctx, query, s.idValue(userID)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// RevokeUserTokens deletes all of a user's device-token rows except the current
// access token. The comparison matches upstream's OrdinalIgnoreCase behavior.
func (s *Identity) RevokeUserTokens(ctx context.Context, userID dto.UUID, currentToken string) (err error) {
	tx, err := s.stores.Writer().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollbackOnReturn(tx, &err)
	rows, err := tx.QueryContext(ctx, "SELECT access_token FROM devices WHERE user_id = "+s.p(1), s.idValue(userID))
	if err != nil {
		return err
	}
	var tokens []string
	for rows.Next() {
		var token string
		if err := rows.Scan(&token); err != nil {
			return closeRows(rows, err)
		}
		if !strings.EqualFold(token, currentToken) {
			tokens = append(tokens, token)
		}
	}
	if err := closeRows(rows, rows.Err()); err != nil {
		return err
	}
	for _, token := range tokens {
		if _, err := tx.ExecContext(ctx, "DELETE FROM devices WHERE access_token = "+s.p(1), token); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// UpsertDeviceActivity applies the AuthorizationContext device side effects. It
// updates supplied name/version and activity only when now-lastActivity is
// strictly greater than staleAfter; exactly three minutes is deliberately stale
// false to match upstream's `TotalMinutes > 3` check.
func (s *Identity) UpsertDeviceActivity(ctx context.Context, device Device, staleAfter time.Duration, now time.Time) (updated Device, didUpdate bool, err error) {
	tx, err := s.stores.Writer().BeginTx(ctx, nil)
	if err != nil {
		return Device{}, false, err
	}
	defer rollbackOnReturn(tx, &err)
	current, err := s.scanDevice(tx.QueryRowContext(ctx, "SELECT id, user_id, access_token, app_name, app_version, device_name, device_id, date_created, date_modified, date_last_activity FROM devices WHERE access_token = "+s.p(1)+s.forUpdate(), device.AccessToken))
	if errors.Is(err, ErrNotFound) {
		created, createErr := s.insertDevice(ctx, tx, device)
		if createErr != nil {
			return Device{}, false, createErr
		}
		if err := tx.Commit(); err != nil {
			return Device{}, false, err
		}
		return created, true, nil
	}
	if err != nil {
		return Device{}, false, err
	}
	// A delayed authorization request must never overwrite metadata written by a
	// newer request. DateModified is the ordering fact for mutable device data.
	if now.Before(current.DateModified) {
		if err := tx.Commit(); err != nil {
			return Device{}, false, err
		}
		return current, false, nil
	}
	update := false
	if device.DeviceName != "" && !strings.EqualFold(current.DeviceName, device.DeviceName) {
		current.DeviceName = device.DeviceName
		update = true
	}
	if device.AppVersion != "" && !strings.EqualFold(current.AppVersion, device.AppVersion) {
		current.AppVersion = device.AppVersion
		update = true
	}
	if now.Sub(current.DateLastActivity) > staleAfter {
		current.DateLastActivity = now
		update = true
	}
	if !update {
		if err := tx.Commit(); err != nil {
			return Device{}, false, err
		}
		return current, false, nil
	}
	current.DateModified = now
	result, err := tx.ExecContext(ctx, "UPDATE devices SET app_version = "+s.p(1)+", device_name = "+s.p(2)+", date_modified = "+s.p(3)+", date_last_activity = "+s.p(4)+" WHERE id = "+s.p(5)+" AND date_modified <= "+s.p(6), current.AppVersion, current.DeviceName, s.timeValue(current.DateModified), s.timeValue(current.DateLastActivity), current.ID, s.timeValue(now))
	if err != nil {
		return Device{}, false, err
	}
	if err := changed(result); err != nil {
		return Device{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return Device{}, false, err
	}
	return current, true, nil
}

// DeviceRefreshDue reports whether RefreshDeviceIfCurrent would write a device
// row. The row must still be the snapshot that requested came from, and the
// request must change the name or version, or find the activity stale. The
// function does no I/O, so a caller can test a reader snapshot before it
// takes the writer.
func DeviceRefreshDue(current, requested Device, staleAfter time.Duration, now time.Time) bool {
	if !current.DateModified.Equal(requested.DateModified) || now.Before(current.DateModified) {
		return false
	}
	return (requested.DeviceName != "" && !strings.EqualFold(current.DeviceName, requested.DeviceName)) ||
		(requested.AppVersion != "" && !strings.EqualFold(current.AppVersion, requested.AppVersion)) ||
		now.Sub(current.DateLastActivity) > staleAfter
}

// RefreshDeviceIfCurrent applies device activity changes only when device is
// still the exact row version returned by the preceding token lookup. Unlike
// UpsertDeviceActivity, it never creates a missing device row.
func (s *Identity) RefreshDeviceIfCurrent(ctx context.Context, device Device, staleAfter time.Duration, now time.Time) (updated Device, didUpdate bool, err error) {
	tx, err := s.stores.Writer().BeginTx(ctx, nil)
	if err != nil {
		return Device{}, false, err
	}
	defer rollbackOnReturn(tx, &err)

	current, err := s.scanDevice(tx.QueryRowContext(ctx, "SELECT id, user_id, access_token, app_name, app_version, device_name, device_id, date_created, date_modified, date_last_activity FROM devices WHERE id = "+s.p(1)+" AND access_token = "+s.p(2)+s.forUpdate(), device.ID, device.AccessToken))
	if err != nil {
		return Device{}, false, err
	}
	if !DeviceRefreshDue(current, device, staleAfter, now) {
		if err := tx.Commit(); err != nil {
			return Device{}, false, err
		}
		return current, false, nil
	}

	if device.DeviceName != "" && !strings.EqualFold(current.DeviceName, device.DeviceName) {
		current.DeviceName = device.DeviceName
	}
	if device.AppVersion != "" && !strings.EqualFold(current.AppVersion, device.AppVersion) {
		current.AppVersion = device.AppVersion
	}
	if now.Sub(current.DateLastActivity) > staleAfter {
		current.DateLastActivity = now
	}
	current.DateModified = now
	if !current.DateModified.After(device.DateModified) {
		current.DateModified = device.DateModified.Add(time.Nanosecond)
	}
	result, err := tx.ExecContext(ctx, "UPDATE devices SET app_version = "+s.p(1)+", device_name = "+s.p(2)+", date_modified = "+s.p(3)+", date_last_activity = "+s.p(4)+" WHERE id = "+s.p(5)+" AND access_token = "+s.p(6)+" AND date_modified = "+s.p(7), current.AppVersion, current.DeviceName, s.timeValue(current.DateModified), s.timeValue(current.DateLastActivity), device.ID, device.AccessToken, s.timeValue(device.DateModified))
	if err != nil {
		return Device{}, false, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return Device{}, false, err
	}
	if n != 0 {
		if err := tx.Commit(); err != nil {
			return Device{}, false, err
		}
		return current, true, nil
	}

	current, err = s.scanDevice(tx.QueryRowContext(ctx, "SELECT id, user_id, access_token, app_name, app_version, device_name, device_id, date_created, date_modified, date_last_activity FROM devices WHERE id = "+s.p(1)+" AND access_token = "+s.p(2), device.ID, device.AccessToken))
	if err != nil {
		return Device{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return Device{}, false, err
	}
	return current, false, nil
}

func (s *Identity) PutDeviceOptions(ctx context.Context, options DeviceOptions) error {
	_, err := s.stores.Writer().ExecContext(ctx, "INSERT INTO device_options (device_id, custom_name) VALUES ("+s.p(1)+", "+s.p(2)+") ON CONFLICT (device_id) DO UPDATE SET custom_name = EXCLUDED.custom_name", options.DeviceID, options.CustomName)
	return err
}

func (s *Identity) GetDeviceOptions(ctx context.Context, deviceID string) (DeviceOptions, error) {
	var options DeviceOptions
	var customName sql.NullString
	if err := s.stores.Reader().QueryRowContext(ctx, "SELECT device_id, custom_name FROM device_options WHERE device_id = "+s.p(1), deviceID).Scan(&options.DeviceID, &customName); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return DeviceOptions{}, ErrNotFound
		}
		return DeviceOptions{}, err
	}
	if customName.Valid {
		options.CustomName = &customName.String
	}
	return options, nil
}

func (s *Identity) APIKeyByToken(ctx context.Context, token string) (APIKey, error) {
	var key APIKey
	var created, activity any
	if err := s.stores.Reader().QueryRowContext(ctx, "SELECT id, name, access_token, date_created, date_last_activity FROM api_keys WHERE access_token = "+s.p(1), token).Scan(&key.ID, &key.Name, &key.AccessToken, &created, &activity); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return APIKey{}, ErrNotFound
		}
		return APIKey{}, err
	}
	var err error
	if key.DateCreated, err = s.stores.Dialect().ScanTime(created); err != nil {
		return APIKey{}, err
	}
	if key.DateLastActivity, err = s.stores.Dialect().ScanTime(activity); err != nil {
		return APIKey{}, err
	}
	return key, nil
}

func (s *Identity) GetDisplayPreference(ctx context.Context, userID, itemID dto.UUID, client string) (DisplayPreference, error) {
	preference := DisplayPreference{UserID: userID, ItemID: itemID, Client: client}
	if err := s.stores.Reader().QueryRowContext(ctx, "SELECT data FROM display_preferences WHERE user_id = "+s.p(1)+" AND item_id = "+s.p(2)+" AND client = "+s.p(3), s.idValue(userID), s.idValue(itemID), client).Scan(&preference.Data); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return DisplayPreference{}, ErrNotFound
		}
		return DisplayPreference{}, err
	}
	return preference, nil
}

func (s *Identity) AccessSchedules(ctx context.Context, userID dto.UUID) ([]AccessSchedule, error) {
	rows, err := s.stores.Reader().QueryContext(ctx, "SELECT id, day_of_week, start_hour, end_hour FROM access_schedules WHERE user_id = "+s.p(1)+" ORDER BY id", s.idValue(userID))
	if err != nil {
		return nil, err
	}
	var schedules []AccessSchedule
	for rows.Next() {
		schedule := AccessSchedule{UserID: userID}
		if err := rows.Scan(&schedule.ID, &schedule.DayOfWeek, &schedule.StartHour, &schedule.EndHour); err != nil {
			return nil, closeRows(rows, err)
		}
		schedules = append(schedules, schedule)
	}
	return schedules, closeRows(rows, rows.Err())
}

func (s *Identity) PutAPIKey(ctx context.Context, key APIKey) (APIKey, error) {
	err := s.stores.Writer().QueryRowContext(ctx, "INSERT INTO api_keys (name, access_token, date_created, date_last_activity) VALUES ("+s.p(1)+", "+s.p(2)+", "+s.p(3)+", "+s.p(4)+") RETURNING id", key.Name, key.AccessToken, s.timeValue(key.DateCreated), s.timeValue(key.DateLastActivity)).Scan(&key.ID)
	if err != nil {
		return APIKey{}, err
	}
	return key, nil
}

// ListAPIKeys returns every API key in creation order.
func (s *Identity) ListAPIKeys(ctx context.Context) ([]APIKey, error) {
	rows, err := s.stores.Reader().QueryContext(ctx, "SELECT id, name, access_token, date_created, date_last_activity FROM api_keys ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []APIKey
	for rows.Next() {
		var key APIKey
		var created, activity any
		if err := rows.Scan(&key.ID, &key.Name, &key.AccessToken, &created, &activity); err != nil {
			return nil, err
		}
		if key.DateCreated, err = s.stores.Dialect().ScanTime(created); err != nil {
			return nil, err
		}
		if key.DateLastActivity, err = s.stores.Dialect().ScanTime(activity); err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

// DeleteAPIKey deletes the key with token. An unknown token is not an error,
// as AuthenticationManager.DeleteApiKey's ExecuteDelete is not.
func (s *Identity) DeleteAPIKey(ctx context.Context, token string) error {
	_, err := s.stores.Writer().ExecContext(ctx, "DELETE FROM api_keys WHERE access_token = "+s.p(1), token)
	return err
}

func (s *Identity) PutDisplayPreference(ctx context.Context, preference DisplayPreference) error {
	_, err := s.stores.Writer().ExecContext(ctx, "INSERT INTO display_preferences (user_id, item_id, client, data) VALUES ("+s.p(1)+", "+s.p(2)+", "+s.p(3)+", "+s.p(4)+") ON CONFLICT (user_id, item_id, client) DO UPDATE SET data = EXCLUDED.data", s.idValue(preference.UserID), s.idValue(preference.ItemID), preference.Client, preference.Data)
	return err
}

func (s *Identity) PutCustomItemDisplayPreference(ctx context.Context, preference CustomItemDisplayPreference) error {
	_, err := s.stores.Writer().ExecContext(ctx, "INSERT INTO custom_item_display_preferences (user_id, item_id, client, key, value) VALUES ("+s.p(1)+", "+s.p(2)+", "+s.p(3)+", "+s.p(4)+", "+s.p(5)+") ON CONFLICT (user_id, item_id, client, key) DO UPDATE SET value = EXCLUDED.value", s.idValue(preference.UserID), s.idValue(preference.ItemID), preference.Client, preference.Key, preference.Value)
	return err
}

func (s *Identity) ReplaceAccessSchedules(ctx context.Context, userID dto.UUID, schedules []AccessSchedule) (err error) {
	tx, err := s.stores.Writer().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollbackOnReturn(tx, &err)
	if _, err = tx.ExecContext(ctx, "DELETE FROM access_schedules WHERE user_id = "+s.p(1), s.idValue(userID)); err != nil {
		return err
	}
	for _, schedule := range schedules {
		if _, err = tx.ExecContext(ctx, "INSERT INTO access_schedules (user_id, day_of_week, start_hour, end_hour) VALUES ("+s.p(1)+", "+s.p(2)+", "+s.p(3)+", "+s.p(4)+")", s.idValue(userID), schedule.DayOfWeek, schedule.StartHour, schedule.EndHour); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Identity) p(index int) string            { return s.stores.Dialect().Placeholder(index) }
func (s *Identity) timeValue(value time.Time) any { return s.stores.Dialect().TimeValue(value.UTC()) }

func (s *Identity) idValue(id dto.UUID) any {
	if s.p(1) == "$1" {
		return id.Dashed()
	}
	return id[:]
}

func scanIdentityUUID(dst *dto.UUID, value any) error {
	switch value := value.(type) {
	case []byte:
		if len(value) == len(dst) {
			copy(dst[:], value)
			return nil
		}
		parsed, err := dto.ParseUUID(string(value))
		if err != nil {
			return fmt.Errorf("scan UUID bytes: %w", err)
		}
		*dst = parsed
		return nil
	case string:
		parsed, err := dto.ParseUUID(value)
		if err != nil {
			return fmt.Errorf("scan UUID string: %w", err)
		}
		*dst = parsed
		return nil
	default:
		return fmt.Errorf("scan UUID: unsupported %T", value)
	}
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func changed(result sql.Result) error {
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func userWriteError(err error) error {
	if strings.Contains(strings.ToLower(err.Error()), "unique") && strings.Contains(strings.ToLower(err.Error()), "normalized_name") {
		return fmt.Errorf("%w: %v", ErrDuplicateName, err)
	}
	return err
}

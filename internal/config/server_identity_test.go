// SPDX-License-Identifier: GPL-2.0-only

package config_test

import (
	"bytes"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deadfin-org/deadfin/internal/config"
	"github.com/deadfin-org/deadfin/internal/dto"
)

func TestServerIdentityFreshRootCreatesValidUUIDv4DeviceFile(t *testing.T) {
	paths := mustPaths(t)

	identity, err := config.LoadOrCreateServerIdentity(paths, "", rand.Reader)
	if err != nil {
		t.Fatalf("LoadOrCreateServerIdentity: %v", err)
	}

	if identity.ID.IsNil() {
		t.Fatal("installation ID is nil")
	}
	if identity.ServerName != "Deadfin" {
		t.Fatalf("ServerName = %q, want %q", identity.ServerName, "Deadfin")
	}
	if identity.Version != "12.1.0" {
		t.Fatalf("Version = %q, want %q", identity.Version, "12.1.0")
	}

	path := filepath.Join(paths.Data, "device.txt")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("device.txt was not created: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("device.txt mode = %o, want 0600", info.Mode().Perm())
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read device.txt: %v", err)
	}
	if !bytes.Equal(raw, []byte(identity.ID.String())) {
		t.Fatalf("device.txt = %q, want %q", string(raw), identity.ID.String())
	}
	if isNotUUIDv4(identity.ID) {
		t.Fatalf("ID %s is not a valid RFC 4122 version-4 UUID", identity.ID.String())
	}
}

func TestServerIdentityReopenReturnsSameID(t *testing.T) {
	paths := mustPaths(t)

	first, err := config.LoadOrCreateServerIdentity(paths, "Configured", rand.Reader)
	if err != nil {
		t.Fatalf("first load: %v", err)
	}
	second, err := config.LoadOrCreateServerIdentity(paths, "Configured", rand.Reader)
	if err != nil {
		t.Fatalf("second load: %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("reopen ID changed: first=%s second=%s", first.ID.String(), second.ID.String())
	}
}

func TestServerIdentitySecondRootReturnsDifferentID(t *testing.T) {
	firstPaths := mustPaths(t)
	secondPaths := mustPaths(t)

	first, err := config.LoadOrCreateServerIdentity(firstPaths, "", rand.Reader)
	if err != nil {
		t.Fatalf("first root: %v", err)
	}
	second, err := config.LoadOrCreateServerIdentity(secondPaths, "", rand.Reader)
	if err != nil {
		t.Fatalf("second root: %v", err)
	}
	if first.ID == second.ID {
		t.Fatalf("two roots produced the same ID: %s", first.ID.String())
	}
}

func TestServerIdentityRetainsValidPreExistingDeviceFile(t *testing.T) {
	paths := mustPaths(t)
	if err := os.MkdirAll(paths.Data, 0o700); err != nil {
		t.Fatalf("create data dir: %v", err)
	}
	const wantHex = "aaaaaaaa11111111bbbbbbbb22222222"
	want, err := dto.ParseUUID(wantHex)
	if err != nil {
		t.Fatalf("parse want UUID: %v", err)
	}
	if err := os.WriteFile(filepath.Join(paths.Data, "device.txt"), []byte(wantHex), 0o600); err != nil {
		t.Fatalf("write device.txt: %v", err)
	}

	identity, err := config.LoadOrCreateServerIdentity(paths, "Server", rand.Reader)
	if err != nil {
		t.Fatalf("LoadOrCreateServerIdentity: %v", err)
	}
	if identity.ID != want {
		t.Fatalf("ID = %s, want retained %s", identity.ID.String(), want.String())
	}
}

func TestServerIdentityReplacesMalformedDeviceFile(t *testing.T) {
	paths := mustPaths(t)
	if err := os.MkdirAll(paths.Data, 0o700); err != nil {
		t.Fatalf("create data dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(paths.Data, "device.txt"), []byte("not-a-uuid"), 0o600); err != nil {
		t.Fatalf("write device.txt: %v", err)
	}

	identity, err := config.LoadOrCreateServerIdentity(paths, "", rand.Reader)
	if err != nil {
		t.Fatalf("LoadOrCreateServerIdentity: %v", err)
	}
	if identity.ID.IsNil() {
		t.Fatal("ID is nil after replacing malformed device.txt")
	}

	raw, err := os.ReadFile(filepath.Join(paths.Data, "device.txt"))
	if err != nil {
		t.Fatalf("read replaced device.txt: %v", err)
	}
	if string(raw) == "not-a-uuid" {
		t.Fatal("malformed device.txt was not replaced")
	}
	parsed, parseErr := dto.ParseUUID(string(raw))
	if parseErr != nil {
		t.Fatalf("replaced device.txt is not a valid UUID: %v", parseErr)
	}
	if parsed != identity.ID {
		t.Fatalf("device.txt %s does not match returned ID %s", string(raw), identity.ID.String())
	}
}

func TestServerIdentityReplacesNilGUIDDeviceFile(t *testing.T) {
	paths := mustPaths(t)
	if err := os.MkdirAll(paths.Data, 0o700); err != nil {
		t.Fatalf("create data dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(paths.Data, "device.txt"), []byte(dto.NilUUID.String()), 0o600); err != nil {
		t.Fatalf("write device.txt: %v", err)
	}

	identity, err := config.LoadOrCreateServerIdentity(paths, "", rand.Reader)
	if err != nil {
		t.Fatalf("LoadOrCreateServerIdentity: %v", err)
	}
	if identity.ID.IsNil() {
		t.Fatal("nil GUID was not replaced")
	}
}

func TestServerIdentityConfiguredNameWinsOverHostname(t *testing.T) {
	paths := mustPaths(t)

	identity, err := config.LoadOrCreateServerIdentity(paths, "My Server", rand.Reader)
	if err != nil {
		t.Fatalf("LoadOrCreateServerIdentity: %v", err)
	}
	if identity.ServerName != "My Server" {
		t.Fatalf("ServerName = %q, want %q", identity.ServerName, "My Server")
	}
}

func TestServerIdentityEmptyConfiguredNameUsesDefaultName(t *testing.T) {
	paths := mustPaths(t)

	identity, err := config.LoadOrCreateServerIdentity(paths, "", rand.Reader)
	if err != nil {
		t.Fatalf("LoadOrCreateServerIdentity: %v", err)
	}
	if identity.ServerName != "Deadfin" {
		t.Fatalf("ServerName = %q, want %q", identity.ServerName, "Deadfin")
	}
}

func TestServerIdentityWhitespaceOnlyConfiguredNameUsesDefaultName(t *testing.T) {
	paths := mustPaths(t)

	identity, err := config.LoadOrCreateServerIdentity(paths, "   ", rand.Reader)
	if err != nil {
		t.Fatalf("LoadOrCreateServerIdentity: %v", err)
	}
	if identity.ServerName != "Deadfin" {
		t.Fatalf("ServerName = %q, want %q", identity.ServerName, "Deadfin")
	}
}

func TestServerIdentityNameUpdateDoesNotChangeID(t *testing.T) {
	paths := mustPaths(t)

	first, err := config.LoadOrCreateServerIdentity(paths, "", rand.Reader)
	if err != nil {
		t.Fatalf("first load: %v", err)
	}
	second, err := config.LoadOrCreateServerIdentity(paths, "New Name", rand.Reader)
	if err != nil {
		t.Fatalf("second load: %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("ID changed after name update: first=%s second=%s", first.ID.String(), second.ID.String())
	}
	if second.ServerName != "New Name" {
		t.Fatalf("ServerName = %q, want %q", second.ServerName, "New Name")
	}
}

func TestServerIdentityUnwritableDataDirReturnsError(t *testing.T) {
	skipIfRoot(t, "root bypasses directory permission checks, so the write cannot be forced to fail")
	paths := mustPaths(t)
	if err := os.MkdirAll(paths.Data, 0o700); err != nil {
		t.Fatalf("create data dir: %v", err)
	}
	if err := os.Chmod(paths.Data, 0o500); err != nil {
		t.Fatalf("chmod data dir read-only: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(paths.Data, 0o700) })

	_, err := config.LoadOrCreateServerIdentity(paths, "", rand.Reader)
	if err == nil {
		t.Fatal("expected error for unwritable data dir, got nil")
	}
	if !strings.Contains(err.Error(), "persist installation identity") {
		t.Fatalf("error = %q, want one about persisting installation identity", err.Error())
	}
}

func TestServerIdentityUnreadableDeviceFileReturnsError(t *testing.T) {
	// Make device.txt a directory instead of a file. os.ReadFile on a
	// directory fails with EISDIR on every Unix, regardless of the
	// effective UID, so the probe stays deterministic in CI (which runs
	// as root and bypasses file-mode permission checks).
	paths := mustPaths(t)
	if err := os.MkdirAll(paths.Data, 0o700); err != nil {
		t.Fatalf("create data dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(paths.Data, "device.txt"), 0o700); err != nil {
		t.Fatalf("create device.txt as directory: %v", err)
	}

	_, err := config.LoadOrCreateServerIdentity(paths, "", rand.Reader)
	if err == nil {
		t.Fatal("expected error for unreadable device.txt, got nil")
	}
	if !strings.Contains(err.Error(), "read installation identity") {
		t.Fatalf("error = %q, want one about reading installation identity", err.Error())
	}
}

func TestServerIdentityFailingEntropyReaderReturnsError(t *testing.T) {
	paths := mustPaths(t)

	_, err := config.LoadOrCreateServerIdentity(paths, "Server", errReader{})
	if err == nil {
		t.Fatal("expected error for failing entropy reader, got nil")
	}
	if !strings.Contains(err.Error(), "generate installation identity") {
		t.Fatalf("error = %q, want one about generating installation identity", err.Error())
	}
}

type errReader struct{}

func (errReader) Read(p []byte) (int, error) {
	return 0, bytes.ErrTooLarge
}

func TestServerIdentityDeterministicUUIDv4FromEntropy(t *testing.T) {
	seed := make([]byte, 16)
	for i := range seed {
		seed[i] = byte(i + 1)
	}
	paths := mustPaths(t)

	first, err := config.LoadOrCreateServerIdentity(paths, "", bytes.NewReader(append(seed, make([]byte, 0)...)))
	if err != nil {
		t.Fatalf("first deterministic load: %v", err)
	}

	secondPaths := mustPaths(t)
	second, err := config.LoadOrCreateServerIdentity(secondPaths, "", bytes.NewReader(append(seed, make([]byte, 0)...)))
	if err != nil {
		t.Fatalf("second deterministic load: %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("same entropy produced different IDs: first=%s second=%s", first.ID.String(), second.ID.String())
	}
}

// isNotUUIDv4 reports whether the UUID does not carry the RFC 4122 version-4
// and variant bits set by newUUIDv4.
func isNotUUIDv4(id dto.UUID) bool {
	return id[6]&0xf0 != 0x40 || id[8]&0xc0 != 0x80
}

// skipIfRoot skips the test when the process runs as root (effective UID 0).
// Root bypasses Unix file-mode permission checks, so a test that depends on a
// mode-0500 directory or mode-0000 file failing cannot produce a deterministic
// failure under root. The CI jail runs as root.
func skipIfRoot(t *testing.T, reason string) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skipf("skipping permission probe under root: %s", reason)
	}
}

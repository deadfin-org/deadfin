// SPDX-License-Identifier: GPL-2.0-only

package buildinfo_test

import (
	"strings"
	"testing"

	"github.com/deadfin-org/deadfin/internal/buildinfo"
	"github.com/deadfin-org/deadfin/internal/config"
)

const (
	sha    = "0123456789abcdef0123456789abcdef01234567"
	sha12  = "0123456789ab"
	wrong1 = "ffffffffffff"
)

func TestCheckedInDefaultsSatisfyRules(t *testing.T) {
	if err := buildinfo.Validate(buildinfo.Current()); err != nil {
		t.Fatalf("Validate(Current()) = %v", err)
	}
	current := buildinfo.Current()
	if current.Channel != "local" || current.Version != "0.0.0-local" || current.Revision != "unknown" || !current.Dirty {
		t.Fatalf("checked-in defaults must be local/0.0.0-local/unknown/dirty; got %+v", current)
	}
}

func TestCompatibilityTargetMirrorsServerIdentity(t *testing.T) {
	if got, want := buildinfo.CompatibilityTarget, config.DefaultServerIdentity().Version; got != want {
		t.Fatalf("CompatibilityTarget = %q, want config.DefaultServerIdentity().Version %q", got, want)
	}
}

func TestValidateRules(t *testing.T) {
	base := func() buildinfo.Identity {
		return buildinfo.Identity{Component: "deadfin-server", Version: "v1.2.3", Channel: "release", Revision: sha, Dirty: false}
	}
	cases := []struct {
		name   string
		mutate func(*buildinfo.Identity)
		reject string
	}{
		{"component", func(id *buildinfo.Identity) { id.Component = "deadfin-web" }, "component"},
		{"rule 1 accept", func(id *buildinfo.Identity) { id.Channel = "master"; id.Version = "dev-" + sha12 }, ""},
		{"rule 1 reject", func(id *buildinfo.Identity) { id.Channel = "nightly" }, "rule 1"},
		{"rule 2 accept", func(id *buildinfo.Identity) {
			id.Channel = "local"
			id.Version = "0.0.0-local"
			id.Revision = "unknown"
			id.Dirty = true
		}, ""},
		{"rule 2 reject", func(id *buildinfo.Identity) { id.Revision = strings.ToUpper(sha) }, "rule 2"},
		{"rule 3 accept", func(id *buildinfo.Identity) {
			id.Channel = "local"
			id.Version = "0.0.0-local"
			id.Revision = "unknown"
			id.Dirty = true
		}, ""},
		{"rule 3 reject", func(id *buildinfo.Identity) { id.Revision = "unknown"; id.Dirty = true }, "rule 3"},
		{"rule 4 accept", func(id *buildinfo.Identity) {
			id.Channel = "train"
			id.Version = "v1.2.3-dev-" + sha12
			id.Dirty = true
		}, ""},
		{"rule 4 reject", func(id *buildinfo.Identity) { id.Dirty = true }, "rule 4"},
		{"rule 4 reject rc", func(id *buildinfo.Identity) { id.Channel = "rc"; id.Version = "0.3.0-rc.1"; id.Dirty = true }, "rule 4"},
		{"rule 5 accept", func(id *buildinfo.Identity) { id.Version = "1.2.3" }, ""},
		{"rule 5 reject", func(id *buildinfo.Identity) { id.Version = "v1.2.3-rc1" }, "rule 5"},
		{"rule 6 accept v", func(id *buildinfo.Identity) { id.Channel = "train"; id.Version = "v0.2.3-dev-" + sha12 }, ""},
		{"rule 6 accept bare", func(id *buildinfo.Identity) { id.Channel = "train"; id.Version = "0.2.3-dev-" + sha12 }, ""},
		{"rule 6 reject prefix", func(id *buildinfo.Identity) { id.Channel = "train"; id.Version = "0.2.3-dev-" + wrong1 }, "rule 6"},
		{"rule 6 reject shape", func(id *buildinfo.Identity) { id.Channel = "train"; id.Version = "v0.2.3" }, "rule 6"},
		{"rule 7 accept", func(id *buildinfo.Identity) { id.Channel = "master"; id.Version = "dev-" + sha12 }, ""},
		{"rule 7 reject", func(id *buildinfo.Identity) { id.Channel = "master"; id.Version = "dev-" + wrong1 }, "rule 7"},
		{"rule 14 accept", func(id *buildinfo.Identity) { id.Channel = "dev"; id.Version = "0.4.0-dev.20261018071500-" + sha12 }, ""},
		{"rule 14 accept dirty", func(id *buildinfo.Identity) {
			id.Channel = "dev"
			id.Version = "0.4.0-dev.20261018071500-" + sha12
			id.Dirty = true
		}, ""},
		{"rule 14 reject prefix", func(id *buildinfo.Identity) { id.Channel = "dev"; id.Version = "0.4.0-dev.20261018071500-" + wrong1 }, "rule 14"},
		{"rule 14 reject patch", func(id *buildinfo.Identity) { id.Channel = "dev"; id.Version = "0.4.1-dev.20261018071500-" + sha12 }, "rule 14"},
		{"rule 14 reject v", func(id *buildinfo.Identity) { id.Channel = "dev"; id.Version = "v0.4.0-dev.20261018071500-" + sha12 }, "rule 14"},
		{"rule 14 reject stamp", func(id *buildinfo.Identity) { id.Channel = "dev"; id.Version = "0.4.0-dev.2026101807150-" + sha12 }, "rule 14"},
		{"rule 14 reject train shape", func(id *buildinfo.Identity) { id.Channel = "dev"; id.Version = "0.4.0-dev-" + sha12 }, "rule 14"},
		{"rule 8 accept", func(id *buildinfo.Identity) { id.Channel = "local"; id.Version = "0.0.0-local" }, ""},
		{"rule 8 reject", func(id *buildinfo.Identity) { id.Channel = "local"; id.Version = "v1.2.3" }, "rule 8"},
		{"rule 13 accept", func(id *buildinfo.Identity) { id.Channel = "rc"; id.Version = "0.3.0-rc.1" }, ""},
		{"rule 13 accept multi-digit", func(id *buildinfo.Identity) { id.Channel = "rc"; id.Version = "10.20.30-rc.12" }, ""},
		{"rule 13 reject v", func(id *buildinfo.Identity) { id.Channel = "rc"; id.Version = "v0.3.0-rc.1" }, "rule 13"},
		{"rule 13 reject final", func(id *buildinfo.Identity) { id.Channel = "rc"; id.Version = "0.3.0" }, "rule 13"},
		{"rule 13 reject zero", func(id *buildinfo.Identity) { id.Channel = "rc"; id.Version = "0.3.0-rc.0" }, "rule 13"},
		{"rule 13 reject leading zero", func(id *buildinfo.Identity) { id.Channel = "rc"; id.Version = "0.3.0-rc.01" }, "rule 13"},
		{"rule 13 reject empty", func(id *buildinfo.Identity) { id.Channel = "rc"; id.Version = "0.3.0-rc." }, "rule 13"},
		{"rule 13 reject other", func(id *buildinfo.Identity) { id.Channel = "rc"; id.Version = "0.3.0-beta.1" }, "rule 13"},
		{"rule 13 reject build", func(id *buildinfo.Identity) { id.Channel = "rc"; id.Version = "0.3.0-rc.1+b" }, "rule 13"},
		{"rule 5 reject rc", func(id *buildinfo.Identity) { id.Version = "0.3.0-rc.1" }, "rule 5"},
		{"rule 12 accept", func(id *buildinfo.Identity) {
			id.Channel = "local"
			id.Version = "0.0.0-local"
			id.Revision = "unknown"
			id.Dirty = true
		}, ""},
		{"rule 12 reject", func(id *buildinfo.Identity) {
			id.Channel = "local"
			id.Version = "0.0.0-local"
			id.Revision = "unknown"
			id.Dirty = false
		}, "rule 12"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := base()
			tc.mutate(&id)
			err := buildinfo.Validate(id)
			if tc.reject == "" {
				if err != nil {
					t.Fatalf("Validate(%+v) = %v, want nil", id, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.reject) {
				t.Fatalf("Validate(%+v) = %v, want error containing %q", id, err, tc.reject)
			}
		})
	}
}

func TestCapabilitiesReturnsFreshSlice(t *testing.T) {
	first := buildinfo.Capabilities()
	if len(first) != 1 || first[0] != "build-info" {
		t.Fatalf("Capabilities() = %v, want [build-info]", first)
	}
	first[0] = "mutated"
	second := buildinfo.Capabilities()
	if second[0] != "build-info" {
		t.Fatalf("Capabilities() shares a backing array: second call returned %v", second)
	}
}

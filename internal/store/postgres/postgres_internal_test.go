// SPDX-License-Identifier: GPL-2.0-only

package postgres

import "testing"

func TestConnectionConfigTurnsJITOffUnlessTheDSNSetsIt(t *testing.T) {
	for _, tc := range []struct {
		dsn  string
		want string
	}{
		{"postgres://deadfin@db/deadfin", "off"},
		{"postgres://deadfin@db/deadfin?jit=on", "on"},
	} {
		config, err := connectionConfig(tc.dsn, "")
		if err != nil {
			t.Fatalf("connectionConfig(%q): %v", tc.dsn, err)
		}
		if got := config.RuntimeParams["jit"]; got != tc.want {
			t.Errorf("connectionConfig(%q) jit = %q, want %q", tc.dsn, got, tc.want)
		}
	}
}

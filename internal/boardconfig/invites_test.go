// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package boardconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInvitePolicyLoadsAndDefaults(t *testing.T) {
	for _, tc := range []struct {
		body  string
		valid bool
	}{
		{"", true},
		{"[invites]\nwho = \"human\"\n", true},
		{"[invites]\nwho = \"coordinator\"\nmax_live = 8\nmax_ttl_s = 3600\n", true},
		{"[invites]\nwho = \"all\"\n", false},
		{"[invites]\nmax_live = -1\n", false},
		{"[invites]\nmax_live = 0\n", false},
		{"[invites]\nmax_live = 1025\n", false},
		{"[invites]\nmax_ttl_s = -1\n", false},
		{"[invites]\nmax_ttl_s = 0\n", false},
		{"[invites]\nmax_ttl_s = 31536001\n", false},
	} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "dibs.toml"), []byte(tc.body), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := Load(dir)
		if (err == nil) != tc.valid {
			t.Fatalf("%q: %v", tc.body, err)
		}
		if tc.body == "" {
			who, capN, ttl := cfg.Invites.IssuancePolicy()
			if who != "any" || capN != 4 || ttl != 604800 {
				t.Fatalf("wrong defaults: %s %d %d", who, capN, ttl)
			}
		}
	}
}

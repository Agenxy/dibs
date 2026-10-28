// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// Explicit false/empty flags must win as well as nonzero values. A flag can
// repair a file value, and the final composition must be validated once.
func TestPublicFlagsOverrideFileBeforeSharedValidation(t *testing.T) {
	for _, tc := range []struct {
		name, section string
		args          []string
		mode, origin  string
	}{
		{"url", `url="https://file.example.com"`, []string{"--public-url", "https://flag.example.com"}, "tls-proxy", "https://flag.example.com"},
		{"empty", `url="https://file.example.com"`, []string{"--public-url="}, "disabled", ""},
		{
			"forward-and-false", "forward=\"http://127.0.0.1:4778\"\naddr=\"0.0.0.0:4778\"\nack_unverified_guest_client=true\nacme_accept_terms=true",
			[]string{"--public-addr", "127.0.0.1:5999", "--public-forward", "http://[::1]:5888", "--ack-unverified-guest-client=false", "--acme-accept-terms=false"},
			"loopback-forward", "http://[::1]:5888",
		},
		{"acme-true", `host="board.example.com"`, []string{"--acme-accept-terms=true"}, "acme", "https://board.example.com"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "dibs.toml"), []byte("[public]\n"+tc.section+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			fs := cloudFlagSet()
			o, _ := registerDaemonFlags(fs)
			if err := fs.Parse(tc.args); err != nil {
				t.Fatal(err)
			}
			cfg, err := loadConfigWithPublicFlags(dir, o, fs)
			if err != nil {
				t.Fatalf("file overruled an explicit flag: %v", err)
			}
			got, err := resolvePublic(o)
			if err != nil || got.Mode != tc.mode || got.URL != tc.origin {
				t.Fatalf("effective public options were not applied: %v %+v", err, got)
			}
			if tc.name == "forward-and-false" && (cfg.Public.AckUnverifiedGuestClient || cfg.Public.ACMEAcceptTerms || got.Addr != "127.0.0.1:5999") {
				t.Fatal("explicit false/address overrides were ignored")
			}
		})
	}
	// The new flags still work with no file, without making unrelated zero
	// settings look as though TOML explicitly wrote them.
	fs := cloudFlagSet()
	o, _ := registerDaemonFlags(fs)
	if err := fs.Parse([]string{"--public-forward", "http://127.0.0.1:4778"}); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfigWithPublicFlags(t.TempDir(), o, fs); err != nil {
		t.Fatal(err)
	}
}

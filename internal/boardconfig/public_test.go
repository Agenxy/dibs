// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package boardconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPublicFileUsesSharedResolverAndRejectsUnknownKeys(t *testing.T) {
	for _, section := range []string{
		`forward="http://127.0.0.1:4778"`,
		`url="https://board.example.com"`,
		"host=\"board.example.com\"\nacme_accept_terms=true",
		"ip=\"2600:1700::1\"\nack_unverified_guest_client=true",
	} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "dibs.toml"), []byte("[public]\n"+section+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := Load(dir)
		if err != nil {
			t.Fatalf("valid public file: %v", err)
		}
		resolved, err := ResolvePublic(cfg.Public)
		if err != nil || resolved.URL == "" || resolved.Mode == "disabled" {
			t.Fatalf("file did not reach the shared resolver: %v %+v", err, resolved)
		}
	}
	for _, section := range []string{
		`forward="http://example.com:4778"`,
		`forward="http://localhost:4778"`,
		`forward="https://127.0.0.1:4778"`,
		`forward="http://127.0.0.1"`,
		`forward="http://127.0.0.1:0"`,
		`forward="http://127.0.0.1:65536"`,
		`forward="http://u:p@127.0.0.1:4778"`,
		`forward="http://127.0.0.1:4778/path"`,
		`forward="http://127.0.0.1:4778?"`,
		`forward="http://127.0.0.1:4778?q=x"`,
		`forward="http://127.0.0.1:4778#x"`,
		"forward=\"http://127.0.0.1:4778\"\naddr=\"0.0.0.0:4778\"",
		"forward=\"http://127.0.0.1:4778\"\naddr=\"localhost:4778\"",
		"forward=\"http://127.0.0.1:4778\"\nurl=\"https://board.example.com\"",
		"forward=\"http://127.0.0.1:4778\"\nacme_accept_terms=true",
		"forward=\"http://127.0.0.1:4778\"\nack_unverified_guest_client=true",
		`host="board.example.com"`,
		`ip="2600:1700::1"`,
		`unexpected="ignored"`,
	} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "dibs.toml"), []byte("[public]\n"+section+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(dir); err == nil {
			t.Fatalf("public file silently accepted %s", section)
		}
	}
}

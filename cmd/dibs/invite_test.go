// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestInviteCommandBuildsOperatorRequestsAndCloudRecipes(t *testing.T) {
	p, err := invitePayload([]string{"cloud-worker", "--ttl", "30d"})
	if err != nil || p["action"] != "mint" || p["ttl_s"] != int64(30*24*time.Hour/time.Second) {
		t.Fatalf("mint: %v %v", p, err)
	}
	defaults, err := invitePayload([]string{"cloud-worker"})
	if err != nil || defaults["ttl_s"] != int64(0) {
		t.Fatalf("CLI overrode the board's lifetime default: %v %v", defaults, err)
	}
	if _, requested := defaults["export"]; requested {
		t.Fatal("ordinary CLI mint requested a private recovery credential")
	}
	exported, destination, err := inviteOptions([]string{"cloud-worker", "--out", "/private/recipe.json"})
	if err != nil || destination != "/private/recipe.json" || exported["export"] != true || exported["ttl_s"] != int64(0) {
		t.Fatal("private export did not request recovery explicitly or overrode board TTL")
	}
	for _, args := range [][]string{{"list"}, {"revoke", "cloud-worker"}, {"revoke", "--issued-by", "parent"}} {
		if _, err := invitePayload(args); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{nil, {"list", "oops"}, {"revoke"}, {"Uppercase"}, {"worker", "--ttl", "0d"}, {"worker", "--ttl", "366d"}, {"worker", "extra"}} {
		if _, err := invitePayload(args); err == nil {
			t.Fatalf("bad invocation accepted: %v", args)
		}
	}
	var out bytes.Buffer
	if err := printInviteRecipe(&out, "cloud-worker", "dibs_inv_TEST", "https://board.example.com"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"claude mcp add --transport http", "https://board.example.com/mcp", `"mcpServers"`, "[mcp_servers.dibs]", "http_headers", "Bearer dibs_inv_TEST", "network allowlist", "as \"cloud-worker\"", "revoke cloud-worker"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("recipe missing %q", want)
		}
	}
}

func TestInviteResultPreservesGuestTrustAndDoesNotRebuildLegacyClientRecipes(t *testing.T) {
	result := map[string]any{
		"name": "guest-worker", "key": "dibs_inv_PRIVATE_TEST", "url": "https://[2600:1700::1]:4778",
		"config": map[string]any{
			"ca_pem":         "-----BEGIN CERTIFICATE-----\nPUBLIC_TEST_CA\n-----END CERTIFICATE-----",
			"ca_spki_sha256": strings.Repeat("a", 64), "verified_clients": []string{}, "address_stability": "operator-asserted",
		},
	}
	out, err := captureStdout(t, func() error { return printInviteResult(result, true) })
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"No native guest client is verified", "PUBLIC_TEST_CA", strings.Repeat("a", 64), "dibs_inv_PRIVATE_TEST", "https://[2600:1700::1]:4778/mcp", "operator-asserted", "not a WAN proof"} {
		if !strings.Contains(out, want) {
			t.Errorf("guest result lost %q", want)
		}
	}
	for _, forbidden := range []string{"claude mcp add", "[mcp_servers.dibs]", "\"mcpServers\""} {
		if strings.Contains(out, forbidden) {
			t.Errorf("offered unverified native-client configuration %q", forbidden)
		}
	}
}

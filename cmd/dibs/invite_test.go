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

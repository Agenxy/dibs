package main

import (
	"strings"
	"testing"
)

// The Codex stanza carries everything the JSON block does.
//
// Both are printed by one recipe from one env map, and the Codex stanza
// spelled its own two variables, so a hub given as a Supgang peer reached
// .mcp.json with DIBS_BOARD_PEER and ~/.codex/config.toml without it. That
// bridge was fixed to the address the recipe printed and could not follow
// the hub when it moved, which the recipe had just promised it would. Round
// eighteen of the pre-release review.
func TestTheCodexStanzaCarriesThePeerTheJSONBlockDoes(t *testing.T) {
	env := map[string]string{
		"DIBS_ADDR": "https://192.168.1.191:4790", "DIBS_DIR": "/home/u/.dibs-hub",
		boardPeerEnv: "a8a37e32",
	}
	got := codexEnv(env)
	for _, want := range []string{
		`DIBS_BOARD_PEER = "a8a37e32"`, `DIBS_ADDR = "https://192.168.1.191:4790"`,
		`DIBS_DIR = "/home/u/.dibs-hub"`, `CODEX_MCP_PROTOCOL_VERSION = "2026-07-28"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the Codex env table %s lacks %s", got, want)
		}
	}
	if !strings.HasPrefix(got, "{ ") || !strings.HasSuffix(got, " }") {
		t.Errorf("not a TOML inline table: %s", got)
	}
}

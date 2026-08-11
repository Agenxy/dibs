package mcp

import (
	"encoding/json"
	"testing"
)

// A client that announces its SDK rather than itself must not be labelled with
// the SDK's name. hermes uses the official Python SDK and arrives as
// {"name":"mcp","version":"0.1.0"}, which read as `harness: mcp` on the board,
// useless on a mixed fleet, and identical for every Python-SDK client.
func TestGenericClientNameFallsBackToTheDeclaredHarness(t *testing.T) {
	params := json.RawMessage(`{"clientInfo":{"name":"mcp","version":"0.1.0"}}`)

	got := agentInfo(params, &toolArgs{Harness: "hermes"}, nil)
	if got == nil {
		t.Fatal("expected agent info")
	}
	if got.Harness != "hermes" {
		t.Errorf("harness = %q, want the declared %q", got.Harness, "hermes")
	}
	// The SDK version is still the truthful version of the thing that produced
	// the handshake; inventing one would be worse than reporting it.
	if got.Version != "0.1.0" {
		t.Errorf("version = %q, want the SDK's %q", got.Version, "0.1.0")
	}
}

// A client that DOES identify itself always wins. It knows its own product
// name, and the fallback is strictly lower-trust.
func TestRealClientNameBeatsASelfReportedHarness(t *testing.T) {
	params := json.RawMessage(`{"clientInfo":{"name":"claude-code","version":"2.1.219"}}`)

	got := agentInfo(params, &toolArgs{Harness: "definitely-not-claude"}, nil)
	if got == nil {
		t.Fatal("expected agent info")
	}
	if got.Harness != "claude-code" {
		t.Errorf("harness = %q, want the client's own %q", got.Harness, "claude-code")
	}
	if got.Version != "2.1.219" {
		t.Errorf("version = %q, want %q", got.Version, "2.1.219")
	}
}

// With neither a usable client name nor a declared harness, the field stays
// empty. An empty harness reads as "unknown"; "mcp" reads as a product.
func TestNoUsableIdentityLeavesHarnessEmpty(t *testing.T) {
	params := json.RawMessage(`{"clientInfo":{"name":"mcp","version":"0.1.0"}}`)

	got := agentInfo(params, &toolArgs{Model: "some-model"}, nil)
	if got == nil {
		t.Fatal("expected agent info from the model field alone")
	}
	if got.Harness != "" {
		t.Errorf("harness = %q, want empty rather than an SDK name", got.Harness)
	}
}

// A harness that connects over plain HTTP sends clientInfo only on the
// handshake: the stateless tools/call that follows carries none. Without the
// session remembering it, every such agent registers anonymously: a live codex
// run showed up on the board as `harness: null`, indistinguishable from a
// hand-rolled script.
func TestSessionHandshakeIdentifiesStatelessClients(t *testing.T) {
	got := agentInfo(json.RawMessage(`{}`), &toolArgs{},
		&clientInfoJSON{Name: "codex", Version: "0.9.1"})
	if got == nil || got.Harness != "codex" || got.Version != "0.9.1" {
		t.Fatalf("session clientInfo not used: %+v", got)
	}
}

// Anything the request itself carries still wins: a per-request _meta clientInfo
// is fresher than whatever the session said when it connected.
func TestRequestIdentityBeatsTheRemembered(t *testing.T) {
	params := json.RawMessage(`{"_meta":{"io.modelcontextprotocol/clientInfo":{"title":"Claude Code","version":"2"}}}`)
	got := agentInfo(params, &toolArgs{}, &clientInfoJSON{Name: "codex", Version: "0.9.1"})
	if got == nil || got.Harness != "Claude Code" {
		t.Fatalf("request identity should win: %+v", got)
	}
}

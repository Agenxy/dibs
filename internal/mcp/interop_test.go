package mcp

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// Agents on different protocol revisions must share one board.
//
// This is the situation Lanes will actually be in for the foreseeable future,
// and it is not hypothetical: as of August 2026 no shipping host negotiates
// 2026-07-28, so the moment one does, a fleet becomes mixed: one agent arriving
// through the stateless core with no handshake, another through the legacy
// initialize exchange, both expecting to see each other.
//
// A dual-version server can fail this in a way that looks fine from either side.
// Each agent registers, each gets a board, each sees itself, and the two are
// partitioned. Every call succeeds. That is the same silent-partition shape as
// two daemons, arrived at through the protocol instead of the process table, and
// it removes the one guarantee the product exists to provide.
//
// So this drives both paths against one server and asserts they meet: visibility
// both ways, mail both ways, claims both ways, and one total order over the
// ledger regardless of which protocol produced an op.
func TestAgentsOnDifferentProtocolVersionsShareOneBoard(t *testing.T) {
	srv, _ := newServer(t)

	// The modern agent: no initialize, ever. Just requests carrying a version.
	modern := registerOn(t, srv, "2026-07-28", "agent-modern")
	// The legacy agent: full handshake first, exactly as today's hosts do.
	out := rpc(t, srv, "", "initialize", map[string]any{
		"protocolVersion": "2025-11-25",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "legacy", "version": "1"},
	})
	if got := out["result"].(map[string]any)["protocolVersion"]; got != "2025-11-25" {
		t.Fatalf("legacy handshake negotiated %v, want 2025-11-25", got)
	}
	legacy := registerOn(t, srv, "2025-11-25", "agent-legacy")

	// Visibility, both directions. ack_board is the agent's board: show_board
	// is the panel's, and reaching for it here once cost an hour chasing a
	// "missing claims" bug that was a wrong tool, not a wrong server.
	modernBoard := ackOn(t, srv, "2026-07-28", modern)
	legacyBoard := ackOn(t, srv, "2025-11-25", legacy)
	if !mentions(modernBoard, "agent-legacy") {
		t.Errorf("the 2026 agent cannot see the 2025 agent: the fleet is partitioned by protocol")
	}
	if !mentions(legacyBoard, "agent-modern") {
		t.Errorf("the 2025 agent cannot see the 2026 agent: the fleet is partitioned by protocol")
	}

	// Mail across the boundary, both directions.
	toolCallOn(t, srv, "2026-07-28", "send_message", map[string]any{
		"token": modern, "to": "agent-legacy", "type": "question",
		"body": "reaching across from the stateless side",
	})
	if !mentions(toolCallOn(t, srv, "2025-11-25", "inbox", map[string]any{"token": legacy}),
		"stateless side") {
		t.Error("mail sent over 2026 never reached the agent on 2025")
	}
	toolCallOn(t, srv, "2025-11-25", "send_message", map[string]any{
		"token": legacy, "to": "agent-modern", "type": "notify",
		"body": "reaching back from the handshake side",
	})
	if !mentions(toolCallOn(t, srv, "2026-07-28", "inbox", map[string]any{"token": modern}),
		"handshake side") {
		t.Error("mail sent over 2025 never reached the agent on 2026")
	}

	// A claim is coordination's hard edge: if it is invisible to half the fleet
	// it is worse than absent, because the agents that cannot see it believe
	// they checked.
	// Absolute, because a claim path must be: the daemon's working directory is
	// not the agent's, so a relative one names a directory neither meant. That is
	// incidental here: this test is about whether the two protocols see one
	// board, but a refused claim would make it pass for the wrong reason.
	toolCallOn(t, srv, "2026-07-28", "claim", map[string]any{
		"token": modern, "path": "/tmp/lanes-interop/internal/core", "mode": "exclusive",
	})
	if !mentions(ackOn(t, srv, "2025-11-25", legacy), "internal/core") {
		t.Error("a claim made over 2026 is invisible to an agent on 2025")
	}

	// One ledger, one order. Divergent serials would mean the two protocols are
	// reading different histories of the same board.
	mSerials := serialsFrom(toolCallOn(t, srv, "2026-07-28", "events_since",
		map[string]any{"token": modern, "since_serial": 0}))
	lSerials := serialsFrom(toolCallOn(t, srv, "2025-11-25", "events_since",
		map[string]any{"token": legacy, "since_serial": 0}))
	if len(mSerials) == 0 {
		t.Fatal("no events observed; this check would be vacuous")
	}
	if !sameOrder(mSerials, lSerials) {
		t.Errorf("the two protocols observe different orderings:\n  2026: %v\n  2025: %v",
			mSerials, lSerials)
	}
}

func registerOn(t *testing.T, srv *httptest.Server, version, name string) string {
	t.Helper()
	out := toolCallOn(t, srv, version, "register_lane",
		map[string]any{"name": name, "session_id": "s-" + name})
	tok, _ := out["token"].(string)
	if tok == "" {
		t.Fatalf("register_lane on %s returned no token: %v", version, out)
	}
	return tok
}

func ackOn(t *testing.T, srv *httptest.Server, version, token string) map[string]any {
	t.Helper()
	return toolCallOn(t, srv, version, "ack_board", map[string]any{"token": token})
}

// toolCallOn is toolCall with the protocol version chosen by the caller, which
// is the whole point here.
func toolCallOn(t *testing.T, srv *httptest.Server, version, name string,
	args map[string]any,
) map[string]any {
	t.Helper()
	out := rpc(t, srv, version, "tools/call", map[string]any{"name": name, "arguments": args})
	result, ok := out["result"].(map[string]any)
	if !ok {
		t.Fatalf("%s on %s: no result: %v", name, version, out)
	}
	content, ok := result["content"].([]any)
	if !ok || len(content) == 0 {
		t.Fatalf("%s on %s: no content: %v", name, version, result)
	}
	text, _ := content[0].(map[string]any)["text"].(string)
	var payload map[string]any
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		t.Fatalf("%s on %s: payload is not JSON: %q", name, version, text)
	}
	return payload
}

// mentions asks whether a payload contains a string anywhere. Deliberately
// coarse: the question here is "did this reach the other side at all", and
// pinning exact field paths would make the test fail on presentation changes
// that no agent would notice.
func mentions(payload map[string]any, needle string) bool {
	blob, err := json.Marshal(payload)
	if err != nil {
		return false
	}
	return needle != "" && strings.Contains(string(blob), needle)
}

func serialsFrom(payload map[string]any) []float64 {
	events, _ := payload["events"].([]any)
	out := make([]float64, 0, len(events))
	for _, e := range events {
		if m, ok := e.(map[string]any); ok {
			if s, ok := m["serial"].(float64); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

// sameOrder compares the shared prefix: the two reads happen at different
// moments, so the later one legitimately sees more. What must never differ is
// the order of what both saw.
func sameOrder(a, b []float64) bool {
	n := min(len(a), len(b))
	if n == 0 {
		return false
	}
	for i := range n {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// The stateless core requires resultType on results; the legacy path must not
// see it.
//
// Found by driving Lanes with the official MCP Python SDK, which rejected
// tools/list outright: "ListToolsResult: resultType. Field required". Every
// hand-rolled check had passed, because both sides of them were written from one
// reading of the spec. That is why this test exists and why it asserts BOTH
// directions.
//
// The absence on the legacy path is not tidiness. Deployed TypeScript and Rust
// SDKs strict-validate results and reject unknown keys, so stamping this on a
// 2025-11-25 answer would break every client in use today in order to satisfy
// one that no shipping host is yet.
func TestResultTypeIsPresentOnlyOnTheStatelessCore(t *testing.T) {
	srv, _ := newServer(t)

	modern := rpc(t, srv, "2026-07-28", "tools/list", map[string]any{})
	got, _ := modern["result"].(map[string]any)["resultType"].(string)
	if got != "complete" {
		t.Errorf("2026-07-28 tools/list resultType = %q, want \"complete\": the reference "+
			"client refuses to parse a modern result without it", got)
	}

	for _, version := range []string{"2025-11-25", "2025-06-18", ""} {
		out := rpc(t, srv, version, "tools/list", map[string]any{})
		if _, present := out["result"].(map[string]any)["resultType"]; present {
			label := version
			if label == "" {
				label = "(no version header)"
			}
			t.Errorf("%s tools/list carries resultType; SDKs on that path strict-validate "+
				"and reject unknown keys", label)
		}
	}
}

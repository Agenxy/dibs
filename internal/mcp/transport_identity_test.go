package mcp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// An agent reattaches by presenting the same identity, whatever transport it
// speaks.
//
// This is the product's oldest failure and the reason the stdio bridge exists:
// an agent cannot carry a secret across a context boundary, so something
// outside the model has to hold it. The bridge held it in a file, which worked
// and welded identity to one transport. An HTTP client has no such process, so
// the same agent on the same board could not reattach at all.
//
// The fix is not a new credential. It is the SAME nonce, presented where a
// harness can actually put one: config headers over HTTP, config env over stdio
// (which the bridge forwards as the header). See identityFromTransport, and
// #33 for why 2026-07-28 is what makes this the right shape.
func TestAnAgentReattachesWithoutTheBridgeRemembering(t *testing.T) {
	srv, cancel := newServer(t)
	defer cancel()

	// First session: the harness holds the nonce, the agent states nothing.
	first := callWithIdentity(t, srv, "n-durable",
		`{"name":"transport-agent","description":"first activation","kind":"persistent"}`)
	id, _ := first["agent_id"].(string)
	if id == "" {
		t.Fatalf("setup: no agent_id in %v", first)
	}

	// Context ends. A new session, same harness config, same blank agent.
	second := callWithIdentity(t, srv, "n-durable",
		`{"name":"transport-agent","description":"second activation","kind":"persistent"}`)

	if got, _ := second["agent_id"].(string); got != id {
		t.Errorf("second activation became %q instead of reattaching to %q. That is "+
			"the sibling failure: a new row for the same role, unable to read a word "+
			"of its predecessor's mail", got, id)
	}

	// And the board carries ONE row, which is the guarantee in the form the
	// operator sees it. Asserted rather than the `reattached` flag, because the
	// daemon reaches the same agent by more than one route (a same-nonce retry
	// inside the TTL answers `resumed`), and pinning the flag would test which
	// route ran rather than that no sibling exists. Nine rows for five roles is
	// what this prevents.
	board, _ := second["board"].(map[string]any)
	agents, _ := board["agents"].([]any)
	rows := 0
	for _, a := range agents {
		m, _ := a.(map[string]any)
		if n, _ := m["name"].(string); n == "transport-agent" {
			rows++
		}
	}
	if rows != 1 {
		t.Errorf("the board carries %d rows named transport-agent, want 1: a second "+
			"activation forked a sibling instead of reattaching", rows)
	}
}

// The agent's own word still wins, and the transport only fills a blank.
func TestATransportIdentityNeverOverridesTheAgentsOwn(t *testing.T) {
	srv, cancel := newServer(t)
	defer cancel()

	mine := callWithIdentity(t, srv, "n-from-harness",
		`{"name":"stater","description":"states its own","kind":"persistent","nonce":"n-from-agent"}`)
	id, _ := mine["agent_id"].(string)
	if id == "" {
		t.Fatalf("setup: %v", mine)
	}
	// Reattaching with the nonce the AGENT chose must work; if the header had
	// overridden it, this registers as a stranger.
	again := callWithIdentity(t, srv, "",
		`{"name":"stater","description":"again","kind":"persistent","nonce":"n-from-agent"}`)
	if got, _ := again["agent_id"].(string); got != id {
		t.Errorf("the header overrode the nonce the agent supplied: %q vs %q", got, id)
	}
}

// A transport identity must NOT stand in for a parent's vouch secret.
//
// `vouch_child` also takes a `nonce`, and there it means the one-time secret a
// PARENT issued for one specific child. Defaulting that from the caller's own
// identity would let any agent vouch for a child it never spawned, turning a
// convenience into a privilege escalation. The default is therefore scoped to
// register and resume, and this is what holds it there.
func TestTheTransportIdentityDoesNotBecomeAVouchSecret(t *testing.T) {
	srv, cancel := newServer(t)
	defer cancel()

	reg := callWithIdentity(t, srv, "n-parent",
		`{"name":"parent","description":"spawns nothing","kind":"persistent"}`)
	tok, _ := reg["token"].(string)
	if tok == "" {
		t.Fatalf("setup: %v", reg)
	}
	// vouch_child with NO nonce, while a transport identity is present. If the
	// default leaked into this call it would arrive carrying "n-parent".
	out := callWithIdentityRaw(t, srv, "n-parent", "vouch_child",
		`{"token":"`+tok+`"}`)
	body, _ := json.Marshal(out)
	if strings.Contains(string(body), "n-parent") {
		t.Errorf("the caller's own identity was supplied as a vouch secret: %s", body)
	}
}

// callWithIdentity registers via tools/call with the nonce presented the way a
// harness config presents one: in transport metadata, not in the arguments.
func callWithIdentity(t *testing.T, srv *httptest.Server, nonce, args string) map[string]any {
	t.Helper()
	return callWithIdentityRaw(t, srv, nonce, "register", args)
}

// callWithIdentityRaw is the same for any tool, so the vouch_child case can use
// it without pretending to register.
func callWithIdentityRaw(t *testing.T, srv *httptest.Server, nonce, tool, args string) map[string]any {
	t.Helper()
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + tool +
		`","arguments":` + args + `}}`
	hreq, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(body))
	hreq.Header.Set("Content-Type", "application/json")
	if nonce != "" {
		hreq.Header.Set(agentNonceHeader, nonce)
	}
	resp, err := srv.Client().Do(hreq)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	// tools/call wraps the payload in content[0].text.
	res, _ := out["result"].(map[string]any)
	if res == nil {
		return out
	}
	content, _ := res["content"].([]any)
	if len(content) == 0 {
		return res
	}
	first, _ := content[0].(map[string]any)
	text, _ := first["text"].(string)
	var inner map[string]any
	if json.Unmarshal([]byte(text), &inner) == nil {
		return inner
	}
	return res
}

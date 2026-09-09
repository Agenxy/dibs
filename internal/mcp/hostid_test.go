package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// THE DAEMON STAMPS WHERE A CALLER IS, and nothing would have noticed if it
// stopped.
//
// This is the same trap as the checkout root one file over: every rule that
// reads a host id is conditioned on it being present, so an empty one makes
// them all politely not apply and the whole feature is absent while every core
// test stays green. Verified by deleting the line in ServeHTTP that stamps the
// context and watching the entire suite pass.
//
// httptest serves on loopback, which is the case under test: nothing off this
// machine can reach loopback, so the daemon's own node id is EVIDENCE for that
// caller rather than a claim, and it is what makes the single-machine board
// authoritative without asking anybody to configure anything.
func TestALoopbackCallerIsStampedWithThisDaemonsIdentity(t *testing.T) {
	srv, eng, _ := newServerWithEngine(t)
	node := eng.NodeID()
	if node == "" {
		t.Fatal("setup: the engine reports no node id, so this test cannot tell a " +
			"stamped agent from an unstamped one")
	}

	callRegister(t, srv.URL, `{"name":"kim","cwd":"/tmp/kim"}`, "")

	b, err := eng.Board(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := hostIDOnBoard(t, b, "kim")
	if got == "" {
		t.Fatalf("the registered agent has no host id, so every claim it takes "+
			"is on an unknown machine and the cross-machine rule silently never "+
			"applies. Board: %v", b["agents"])
	}
	if got != node {
		t.Errorf("a loopback caller was stamped %q rather than this daemon's own "+
			"node id %q, so agents on ONE machine would read as being on two and "+
			"stop colliding", got, node)
	}
}

// AND WHAT THE CALLER SAYS DOES NOT MOVE IT.
//
// The host rule removes collisions, so a value an agent controls decides which
// other agents it stops colliding with. Over loopback the daemon knows better
// than any assertion could, and taking the assertion anyway would let one agent
// opt out of the conflicts it is supposed to be reporting.
func TestALoopbackCallerCannotAssertADifferentMachine(t *testing.T) {
	srv, eng, _ := newServerWithEngine(t)
	node := eng.NodeID()

	callRegister(t, srv.URL, `{"name":"kim","cwd":"/tmp/kim"}`,
		`,"_meta":{"`+HostMetaKey+`":"somewhere-else"}`)

	b, err := eng.Board(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := hostIDOnBoard(t, b, "kim"); got != node {
		t.Errorf("a caller on loopback claimed to be on machine %q and the board "+
			"believed it. An agent that can name its own machine can excuse itself "+
			"from every path collision on this board", got)
	}
}

// isLoopback is what decides between evidence and assertion, so its answer for
// a malformed or unfamiliar address has to be the conservative one.
func TestOnlyARealLoopbackAddressCountsAsProof(t *testing.T) {
	for _, c := range []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:4777", true},
		{"[::1]:4777", true},
		{"127.2.3.4:80", true}, // the whole 127/8 block is loopback
		{"192.168.1.10:4777", false},
		{"10.0.0.1:4777", false},
		{"", false},
		{"garbage", false},
		// A NAME, NOT AN ADDRESS. Resolving it would be a DNS call on the request
		// path, and a caller who controls their own resolver would control the
		// answer, so it is not proof of anything.
		{"localhost:4777", false},
	} {
		if got := isLoopback(c.addr); got != c.want {
			t.Errorf("isLoopback(%q) = %v, want %v", c.addr, got, c.want)
		}
	}
}

// callRegister posts one tools/call register, with optional extra JSON spliced
// into the params object.
func callRegister(t *testing.T, url, args, extraParams string) {
	t.Helper()
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"register",` +
		`"arguments":` + args + extraParams + `}}`
	resp, err := http.Post(url, "application/json", bytes.NewBufferString(body)) //nolint:noctx // test
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if e, bad := out["error"]; bad {
		t.Fatalf("register was refused, so this test never reached what it measures: %v", e)
	}
}

func hostIDOnBoard(t *testing.T, b map[string]any, id string) string {
	t.Helper()
	agents, _ := b["agents"].([]map[string]any)
	for _, a := range agents {
		if a["id"] != id {
			continue
		}
		// The board carries the struct itself, not a decoded map: Board() sets
		// lm["agent"] = l.Agent.
		info, _ := a["agent"].(*core.AgentInfo)
		if info == nil {
			t.Fatalf("agent %s carries no identity at all", id)
		}
		return info.HostID
	}
	t.Fatalf("agent %s is not on the board, so the setup did not happen", id)
	return ""
}

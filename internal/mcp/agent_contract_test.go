package mcp

import (
	"encoding/json"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// board promises one cheap summary line because agents call it repeatedly,
// but the complete board was also returned in structuredContent. Hosts that put
// that base-MCP field into model context charged the agent for every agent and
// slot despite the promise saying the detail went only to the human panel.
//
// The default must keep board detail out of the whole model-facing result. This
// deliberately does NOT remove access to the JSON: detail=true is the explicit
// choice for an agent that needs it.
func TestShowBoardDefaultsToOneSummaryAndMakesDetailExplicit(t *testing.T) {
	srv, _ := newServer(t)
	const marker = "THIS DESCRIPTION MUST NOT ENTER DEFAULT MODEL CONTEXT"
	registered := toolCall(t, srv, "register", map[string]any{
		"name": "context-cost", "description": marker,
	})
	token := registered["token"].(string)

	result := rawToolResult(t, srv, "board", map[string]any{"token": token})
	content := result["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("default content blocks = %d, want one summary line", len(content))
	}
	text, _ := content[0].(map[string]any)["text"].(string)
	if !strings.HasPrefix(text, "Dibs board:") {
		t.Fatalf("default content is not the promised summary: %q", text)
	}
	modelFacing := map[string]any{"content": result["content"]}
	if structured, ok := result["structuredContent"]; ok {
		modelFacing["structuredContent"] = structured
	}
	encoded, _ := json.Marshal(modelFacing)
	if strings.Contains(string(encoded), marker) {
		t.Fatal("default board still returns the full board outside its summary")
	}
	meta, _ := result["_meta"].(map[string]any)
	encoded, _ = json.Marshal(meta)
	if !strings.Contains(string(encoded), marker) {
		t.Fatal("default board withheld board detail from the human panel as well as the model")
	}

	detailed := rawToolResult(t, srv, "board", map[string]any{
		"token": token, "detail": true,
	})
	encoded, _ = json.Marshal(detailed)
	if !strings.Contains(string(encoded), marker) {
		t.Fatal("detail=true did not return the board JSON the agent explicitly requested")
	}
}

// check_in is the recovery checkpoint after context loss. Both content shapes
// must answer what the agent still owes and what was done to it, including with
// empty arrays: absence is indistinguishable from a broken checkpoint. The data
// already exists upstream; this test guards the MCP projection that dropped it.
//
// This deliberately checks both ordinary content and structuredContent because
// real hosts choose different carriers when presenting a tool result to a model.
func TestAckBoardKeepsRecoveryKeysInEveryModelFacingShape(t *testing.T) {
	srv, _ := newServer(t)
	registered := toolCall(t, srv, "register", map[string]any{"name": "recovering"})
	token := registered["token"].(string)

	result := rawToolResult(t, srv, "check_in", map[string]any{"token": token})
	content := result["content"].([]any)[0].(map[string]any)["text"].(string)
	var plain map[string]any
	if err := json.Unmarshal([]byte(content), &plain); err != nil {
		t.Fatalf("check_in content is not JSON: %v", err)
	}
	assertEmptyCheckpointLists(t, "content", plain)

	// The rule is about half-answers. A host picks a carrier, so any shape it
	// might show the model must answer everything the checkpoint owes: a
	// structuredContent carrying SOME of it would present a checkpoint with its
	// obligations silently missing, which is indistinguishable from having none.
	if structured, ok := result["structuredContent"].(map[string]any); ok {
		assertEmptyCheckpointLists(t, "structuredContent", structured)
	}
}

// A count tells an agent it is not alone and withholds the only actionable
// fact: who to coordinate with. read_space returned members:2 even though the
// board already carried both identities. Keep the count for compatibility and
// add names at the MCP boundary; this deliberately does NOT mutate membership
// or acknowledge anything merely because the agent was read.
func TestLaneReadNamesItsMembers(t *testing.T) {
	srv, _ := newServer(t)
	alpha := toolCall(t, srv, "register", map[string]any{"name": "alpha"})
	beta := toolCall(t, srv, "register", map[string]any{"name": "beta"})
	ta, tb := alpha["token"].(string), beta["token"].(string)
	toolCall(t, srv, "check_in", map[string]any{"token": ta})
	toolCall(t, srv, "check_in", map[string]any{"token": tb})
	toolCall(t, srv, "open_space", map[string]any{
		"token": ta, "space": "shared-work", "topic": "coordinate the shared work",
	})
	toolCall(t, srv, "join_space", map[string]any{"token": tb, "space": "shared-work"})

	read := toolCall(t, srv, "read_space", map[string]any{"token": ta, "space": "shared-work"})
	rawNames, ok := read["member_names"].([]any)
	if !ok {
		t.Fatalf("read_space member_names = %T, want an array of agent names; result: %v",
			read["member_names"], read)
	}
	names := make([]string, 0, len(rawNames))
	for _, name := range rawNames {
		if s, ok := name.(string); ok {
			names = append(names, s)
		}
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"alpha", "beta"}) {
		t.Fatalf("read_space member_names = %v, want alpha and beta", names)
	}
	if read["members"] != float64(2) {
		t.Fatalf("read_space members = %v, want the compatible count 2", read["members"])
	}
}

func rawToolResult(t *testing.T, srv *httptest.Server, name string, args map[string]any) map[string]any {
	t.Helper()
	out := rpc(t, srv, "2026-07-28", "tools/call", map[string]any{"name": name, "arguments": args})
	result, ok := out["result"].(map[string]any)
	if !ok {
		t.Fatalf("%s: no tool result: %v", name, out)
	}
	if isError, _ := result["isError"].(bool); isError {
		t.Fatalf("%s returned an error: %v", name, result)
	}
	return result
}

func assertEmptyCheckpointLists(t *testing.T, carrier string, result map[string]any) {
	t.Helper()
	for _, key := range []string{"announcements", "agent_updates"} {
		value, present := result[key]
		if !present {
			t.Errorf("%s omits %s", carrier, key)
			continue
		}
		items, ok := value.([]any)
		if !ok {
			t.Errorf("%s %s = %T, want an empty array", carrier, key, value)
			continue
		}
		if len(items) != 0 {
			t.Errorf("%s %s = %v, want empty on a fresh agent", carrier, key, items)
		}
	}
}

// A host may show the model structuredContent INSTEAD of content, and this one
// does. So a checkpoint tool must never put anything in structuredContent that
// answers less than content does: the agent would silently read the lesser
// shape and believe it was the answer.
//
// This is not hypothetical and it is not old. A panel bootstrap of three
// plumbing fields was added here, and calling check_in as an ordinary agent
// returned the token and the view and NOTHING about the fleet: no board, no
// mail, nothing owed. Every existing assertion still passed, because the
// checkpoint really was present: in the field this host does not display.
//
// The rule is therefore about the whole result, not about any one carrier: what
// the agent needs must be in content, and nothing may sit beside content
// claiming to be the result while saying less.
func TestACheckpointIsNeverReplacedByASmallerShape(t *testing.T) {
	srv, _ := newServer(t)
	registered := toolCall(t, srv, "register", map[string]any{"name": "checkpointing"})
	token := registered["token"].(string)

	for _, tool := range []string{"check_in", "inbox"} {
		result := rawToolResult(t, srv, tool, map[string]any{"token": token})
		text := result["content"].([]any)[0].(map[string]any)["text"].(string)
		var plain map[string]any
		if err := json.Unmarshal([]byte(text), &plain); err != nil {
			t.Fatalf("%s content is not JSON: %v", tool, err)
		}
		// And the panel's own needs must travel in that same shape, or the fix
		// for one host breaks the panel on it.
		if plain["act_token"] != token {
			t.Errorf("%s content carries no act_token; the panel cannot act on a host "+
				"that forwards neither _meta nor structuredContent", tool)
		}

		// structuredContent must be the SAME answer, key for key: never a
		// smaller one. This was written as "must be absent", which was the right
		// instinct aimed at the wrong target: what harms the agent is a shape
		// beside content that says LESS, because a host may show that one
		// instead. An identical shape cannot, and it is the only carrier that
		// reaches a panel the client cached before a fix.
		structured, present := result["structuredContent"].(map[string]any)
		if !present {
			continue
		}
		for k := range plain {
			if _, ok := structured[k]; !ok {
				t.Errorf("%s structuredContent omits %q that content answers; a host "+
					"showing structuredContent would give the agent the lesser answer", tool, k)
			}
		}
		// And what the cached panel actually reads, for the tools that have it.
		if _, hasBoard := plain["board"]; hasBoard {
			if _, ok := structured["board"]; !ok {
				t.Errorf("%s structuredContent carries no board; a panel cached before "+
					"the content carrier existed has nothing to draw", tool)
			}
		}
	}

	// check_in specifically must still answer everything it owes.
	result := rawToolResult(t, srv, "check_in", map[string]any{"token": token})
	text := result["content"].([]any)[0].(map[string]any)["text"].(string)
	var plain map[string]any
	if err := json.Unmarshal([]byte(text), &plain); err != nil {
		t.Fatalf("check_in content is not JSON: %v", err)
	}
	assertEmptyCheckpointLists(t, "content", plain)
}

// A relative claim path is refused, because the daemon's working directory is
// not the agent's.
//
// canonPath runs inside dibd, started wherever it was started. `/` under
// launchd. So claim(path:"internal/mcp") was canonicalised to "/internal/mcp":
// a directory that exists nowhere, that no other agent will ever name, and that
// overlaps nothing. The call answered granted:true and the board displayed the
// claim, so the agent believed it held exclusive access to a directory it had
// never claimed. A coordination primitive reporting success for a no-op is the
// worst way this mechanism can fail: every other agent is respecting a claim
// that is not there.
//
// Refused rather than resolved against the caller's cwd, deliberately: a claim
// is what OTHER agents are asked to respect, and silently rewriting one agent's
// shorthand into an absolute path the reader would not have guessed is how the
// guard's alias bug happened once already.
func TestARelativeClaimPathIsRefusedRatherThanGuessedAt(t *testing.T) {
	srv, _ := newServer(t)
	registered := toolCall(t, srv, "register", map[string]any{"name": "claimer"})
	token := registered["token"].(string)
	toolCall(t, srv, "check_in", map[string]any{"token": token})

	out := rpc(t, srv, "2026-07-28", "tools/call", map[string]any{
		"name":      "claim",
		"arguments": map[string]any{"token": token, "path": "internal/mcp", "mode": "exclusive"},
	})
	result, _ := out["result"].(map[string]any)
	if result == nil {
		t.Fatalf("no result: %v", out)
	}
	if isError, _ := result["isError"].(bool); !isError {
		t.Fatalf("a relative claim path was accepted: %v", result)
	}
	text, _ := result["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "E_RELATIVE_PATH") {
		t.Errorf("refusal does not name the code: %s", text)
	}
	// The hint must say what to type instead, not merely that this was wrong.
	if !strings.Contains(text, "absolute") {
		t.Errorf("refusal does not name the corrective action: %s", text)
	}

	// An absolute path still works, so this is a guard rather than a wall.
	ok := toolCall(t, srv, "claim", map[string]any{
		"token": token, "path": "/tmp/agents-claim-probe", "mode": "shared",
	})
	if ok["granted"] != true {
		t.Errorf("an absolute claim was refused too: %v", ok)
	}
}

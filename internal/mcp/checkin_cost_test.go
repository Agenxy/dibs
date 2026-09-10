package mcp

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// THE ONE CALL EVERY AGENT MUST MAKE IS NOT THE MOST EXPENSIVE THING IT CAN DO.
//
// check_in is required once per activation, and its board dominated what it
// cost: 41 KB of a 71 KB checkpoint on this project's own 35-agent board, each
// row carrying the whole identity block, the repository's root commits and
// every slot's full text. `board`, the call an agent may skip, returned one
// summary line. An agent that learns the mandatory call costs twelve thousand
// tokens makes it as rarely as it can get away with, which is the exact
// disengagement #53 wanted reminders for. Issue #55.
//
// So the board on check_in is one row per agent unless detail is asked for,
// the human's panel still gets everything from _meta, and everything the call
// OWES (mail, announcements, updates, the cursor) is untouched.
func TestCheckInChargesForARosterNotTheWholeBoard(t *testing.T) {
	srv, _ := newServer(t)
	// A board with a few agents whose declarations are long, the way real ones
	// are: the cost is in the text, not the row count.
	long := strings.Repeat("a paragraph of declaration text that nobody reads on check_in. ", 12)
	var tok string
	for i, name := range []string{"one", "two", "three", "four"} {
		r := toolCall(t, srv, "register", map[string]any{
			"name": name, "description": long, "cwd": "/tmp/" + name,
		})
		tk, _ := r["token"].(string)
		toolCall(t, srv, "check_in", map[string]any{"token": tk})
		toolCall(t, srv, "declare", map[string]any{"token": tk, "text": long})
		if i == 0 {
			tok = tk
		}
	}

	slim := toolCallText(t, srv, "check_in", map[string]any{"token": tok})
	full := toolCallText(t, srv, "check_in", map[string]any{"token": tok, "detail": true})
	if len(full) <= len(slim) {
		t.Fatalf("detail=true (%d chars) is no larger than the default (%d): the default "+
			"is still the whole board and the mandatory call still costs what it did",
			len(full), len(slim))
	}
	if ratio := float64(len(slim)) / float64(len(full)); ratio > 0.5 {
		t.Errorf("the default check_in is %.0f%% the size of the full one; on this board "+
			"most of the payload was declaration text and identity, and the roster "+
			"should have dropped nearly all of it", ratio*100)
	}

	// Still a BOARD, in shape: agents by id and status, so an agent that reads
	// board.agents[i].id is not broken by the trim or by turning detail on.
	var out struct {
		Board struct {
			Agents []map[string]any `json:"agents"`
			Claims []map[string]any `json:"claims"`
		} `json:"board"`
		Inbox any `json:"inbox"`
	}
	if err := json.Unmarshal([]byte(slim), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Board.Agents) != 4 {
		t.Fatalf("the roster names %d agents, want 4: a trim that drops rows is not a "+
			"summary, it is a lie about who is here", len(out.Board.Agents))
	}
	for _, a := range out.Board.Agents {
		if a["id"] == "" || a["status"] == "" {
			t.Errorf("a roster row lacks id or status: %v", a)
		}
		if doing, _ := a["doing"].(string); doing == "" {
			t.Errorf("a roster row for an agent with a declaration says nothing about "+
				"what it is doing, which is the one thing orientation is for: %v", a)
		} else if len(doing) > 160 {
			t.Errorf("the roster carries %d chars of declaration for one agent; it was "+
				"meant to be one line", len(doing))
		}
		if _, whole := a["agent"]; whole {
			t.Errorf("the roster row still carries the full identity block: %v", a)
		}
	}
}

// A claim is coordination's hard edge, and it stays visible in the roster.
//
// An agent that checked in and could not see that a directory is held
// exclusively would believe it had looked. Whole in shape, without the note
// and the timestamps.
func TestTheRosterStillShowsClaims(t *testing.T) {
	srv, _ := newServer(t)
	r := toolCall(t, srv, "register", map[string]any{"name": "holder", "cwd": "/tmp/h"})
	tok, _ := r["token"].(string)
	toolCall(t, srv, "check_in", map[string]any{"token": tok})
	toolCall(t, srv, "claim", map[string]any{
		"token": tok, "path": "/tmp/h/internal/core", "mode": "exclusive", "note": "mine",
	})
	r2 := toolCall(t, srv, "register", map[string]any{"name": "other", "cwd": "/tmp/o"})
	tok2, _ := r2["token"].(string)

	text := toolCallText(t, srv, "check_in", map[string]any{"token": tok2})
	if !strings.Contains(text, "/tmp/h/internal/core") || !strings.Contains(text, "exclusive") {
		t.Fatalf("an exclusive claim is invisible on check_in's roster:\n%s", text[:min(len(text), 600)])
	}
}

// toolCallText is toolCall's model-facing text, unparsed: the bytes the model
// is actually charged for.
func toolCallText(t *testing.T, srv *httptest.Server, tool string, args map[string]any) string {
	t.Helper()
	out := rpc(t, srv, "2026-07-28", "tools/call", map[string]any{"name": tool, "arguments": args})
	result, ok := out["result"].(map[string]any)
	if !ok {
		t.Fatalf("%s: no result: %v", tool, out)
	}
	content, _ := result["content"].([]any)
	if len(content) == 0 {
		t.Fatalf("%s returned no content", tool)
	}
	text, _ := content[0].(map[string]any)["text"].(string)
	return text
}

package mcp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// The panel opens when somebody asks for it, and at no other time.
//
// It used to ride along on every call that carried board or mailbox state:
// check_in, inbox, send, respond, await_events. check_in is made once per
// activation, so the operator saw the panel open on every turn of every agent,
// and asked for it to appear only when the agent calls it or the user invokes
// it. Hosts decide from the tool DECLARATION as well as the result, so both
// are checked.
func TestOnlyBoardDrawsThePanel(t *testing.T) {
	srv, _ := newServer(t)

	listed := rpc(t, srv, "2026-07-28", "tools/list", map[string]any{})
	tools, _ := listed["result"].(map[string]any)["tools"].([]any)
	var declaring []string
	for _, raw := range tools {
		tool, _ := raw.(map[string]any)
		meta, _ := tool["_meta"].(map[string]any)
		if _, has := meta["ui"]; has {
			declaring = append(declaring, tool["name"].(string))
		}
	}
	if strings.Join(declaring, ",") != "board" {
		t.Errorf("tools declaring the panel: %v, want only board", declaring)
	}

	a := toolCall(t, srv, "register", map[string]any{"name": "panel-a", "description": "a"})
	b := toolCall(t, srv, "register", map[string]any{"name": "panel-b", "description": "b"})
	tokA, tokB := a["token"].(string), b["token"].(string)
	toolCall(t, srv, "check_in", map[string]any{"token": tokB})
	toolCall(t, srv, "send", map[string]any{
		"token": tokA, "to": b["agent_id"], "type": "question", "body": "there?",
	})

	for _, c := range []struct {
		name string
		args map[string]any
	}{
		{"check_in", map[string]any{"token": tokA}},
		{"inbox", map[string]any{"token": tokB}},
		{"send", map[string]any{"token": tokB, "to": a["agent_id"], "type": "notify", "body": "yes"}},
		{"await_events", map[string]any{"token": tokA, "since_serial": 0, "timeout_s": 0}},
	} {
		res := rawToolResult(t, srv, c.name, c.args)
		meta, _ := res["_meta"].(map[string]any)
		if _, has := meta["ui"]; has {
			t.Errorf("%s drew the panel: %v", c.name, meta["ui"])
		}
		if _, has := meta[panelDataMetaKey]; has {
			t.Errorf("%s carried a panel payload nobody asked for", c.name)
		}
	}

	shown := rawToolResult(t, srv, "board", map[string]any{"token": tokA})
	meta, _ := shown["_meta"].(map[string]any)
	ui, _ := meta["ui"].(map[string]any)
	if ui["resourceUri"] != uiBoardURI {
		t.Fatalf("board did not draw the panel: %v", shown["_meta"])
	}
}

// The Activity tab has something in it on the panel people actually open.
//
// It was filled only from await_events, which returns events as its answer,
// and an agent reached from a notification (a ChatGPT thread) never makes that
// call, so the tab was blank on every panel the operator looked at.
func TestTheBoardPanelCarriesRecentActivity(t *testing.T) {
	srv, _ := newServer(t)
	a := toolCall(t, srv, "register", map[string]any{"name": "act-a", "description": "a"})
	b := toolCall(t, srv, "register", map[string]any{"name": "act-b", "description": "b"})
	c := toolCall(t, srv, "register", map[string]any{"name": "act-c", "description": "c"})
	tokA := a["token"].(string)
	toolCall(t, srv, "check_in", map[string]any{"token": tokA})
	toolCall(t, srv, "send", map[string]any{
		"token": tokA, "to": b["agent_id"], "type": "notify", "body": "for b",
	})
	// Mail between two OTHER agents is not this agent's activity.
	toolCall(t, srv, "check_in", map[string]any{"token": c["token"]})
	toolCall(t, srv, "send", map[string]any{
		"token": c["token"], "to": b["agent_id"], "type": "notify", "body": "c to b",
	})

	shown := rawToolResult(t, srv, "board", map[string]any{"token": tokA, "view": "activity"})
	meta, _ := shown["_meta"].(map[string]any)
	payload, _ := meta[panelDataMetaKey].(map[string]any)
	events := asMaps(payload["events"])
	if len(events) == 0 {
		t.Fatal("the board panel carried no activity")
	}
	var sentByA, sentByC bool
	for _, ev := range events {
		if strings.HasPrefix(ev["type"].(string), "message.") {
			sentByA = sentByA || ev["agent"] == a["agent_id"]
			sentByC = sentByC || ev["agent"] == c["agent_id"]
		}
	}
	if !sentByA {
		t.Errorf("the caller's own message is missing from its activity: %v", events)
	}
	if sentByC {
		t.Errorf("activity shows mail between two other agents: %v", events)
	}
	if payload["view"] != "activity" {
		t.Errorf("view = %v, want activity", payload["view"])
	}
}

// A person opens the panel through the board prompt, which hosts list as a
// slash command or a menu entry.
func TestTheBoardPromptAsksForTheBoard(t *testing.T) {
	srv, _ := newServer(t)
	disc := rpc(t, srv, "2026-07-28", "server/discover", map[string]any{})
	caps, _ := disc["result"].(map[string]any)["capabilities"].(map[string]any)
	if _, ok := caps["prompts"]; !ok {
		t.Fatal("prompts capability not advertised, so no host will list the prompt")
	}
	listed := rpc(t, srv, "2026-07-28", "prompts/list", map[string]any{})
	prompts, _ := listed["result"].(map[string]any)["prompts"].([]any)
	if len(prompts) != 1 || prompts[0].(map[string]any)["name"] != "board" {
		t.Fatalf("prompts/list = %v, want the board prompt", listed)
	}
	for view, want := range map[string]string{"": "board tool", "mail": `view "mail"`} {
		args := map[string]any{}
		if view != "" {
			args["view"] = view
		}
		got := rpc(t, srv, "2026-07-28", "prompts/get", map[string]any{"name": "board", "arguments": args})
		msgs, _ := got["result"].(map[string]any)["messages"].([]any)
		if len(msgs) != 1 {
			t.Fatalf("view %q: %v", view, got)
		}
		text, _ := msgs[0].(map[string]any)["content"].(map[string]any)["text"].(string)
		if !strings.Contains(text, want) {
			t.Errorf("view %q: prompt %q does not ask for %q", view, text, want)
		}
	}
	bad := rpc(t, srv, "2026-07-28", "prompts/get", map[string]any{
		"name": "board", "arguments": map[string]any{"view": "everything"},
	})
	if _, isErr := bad["error"]; !isErr {
		t.Errorf("an unknown view was accepted: %v", bad)
	}
}

// A task's progress notes are text the recipient wrote, so the panel copy that
// travels through the host blanks them like bodies, in either shape a message
// reaches the redactor. The milestone numbers stay: the panel counts them.
func TestProgressNotesDoNotTravelInThePanelCopy(t *testing.T) {
	typed := &core.Message{Body: "b", Progress: []core.Progress{{Milestone: 1, Note: "SECRET NOTE"}}}
	normalised := map[string]any{"messages": []any{map[string]any{
		"body": "b", "progress": []any{map[string]any{"milestone": 1, "note": "SECRET NOTE"}},
	}}}
	for name, v := range map[string]any{"typed": core.Result{"m": typed}, "normalised": core.Result(normalised)} {
		out, _ := json.Marshal(withoutBodies(v.(core.Result)))
		if strings.Contains(string(out), "SECRET NOTE") {
			t.Errorf("%s: a progress note reached the panel copy: %s", name, out)
		}
		if !strings.Contains(string(out), `"milestone":1`) {
			t.Errorf("%s: the milestone number was lost: %s", name, out)
		}
	}
	if typed.Progress[0].Note != "SECRET NOTE" {
		t.Error("redaction blanked the agent's own copy")
	}
}

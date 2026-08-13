package mcp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// The payload must carry only what board_app.html draws. It lives in the App's
// private metadata rather than model context, but it still rides every panel
// result; invisible transport is not permission for unbounded transport.
func TestPanelPayloadCarriesOnlyRenderedFields(t *testing.T) {
	in := core.Result{
		"agent_id": "opus-5",
		"view":     "mail",
		"board": core.Result{
			"node": "n1", "serial": 42,
			"agents": []core.Result{{
				"id": "opus-5", "name": "opus-5", "kind": "ephemeral", "status": "active",
				"description": "d", "last_coordination_at": "t", "agent": map[string]any{"model": "m"},
				// none of these are drawn:
				"activation": 3, "acked_serial": 7, "proc_alive": true, "last_seen": "t", "pid": 999,
				"slots": []core.Result{{
					"id": "s1", "text": "w", "refs": []string{"r"},
					"updated_serial": 12,
				}},
			}},
		},
		"inbox": core.Result{"messages": []core.Result{{
			"serial": 1, "type": "question", "from": "a", "to": "b", "body": "x",
			"deadline": "t", "delivered_serial": 4, "consumed": false,
		}}},
		"acked_serial": 9, "ok": true, "truncated_before_serial": 0,
	}
	out := panelPayload(in)
	blob, _ := json.Marshal(out)
	s := string(blob)

	for _, leaked := range []string{
		"activation", "acked_serial", "proc_alive", "last_seen",
		"pid", "updated_serial", "deadline", "delivered_serial", "consumed", "truncated_before_serial",
	} {
		if strings.Contains(s, leaked) {
			t.Errorf("payload leaks %q: it is not drawn by the panel", leaked)
		}
	}
	for _, needed := range []string{"opus-5", "active", "question", "agent_id", "view"} {
		if !strings.Contains(s, needed) {
			t.Errorf("payload dropped %q, which the panel renders", needed)
		}
	}
	// Trimming must not silently empty the board: the failure that shipped once
	// was a core.Result assertion against a plain map, which dropped every agent.
	b := asMap(out["board"])
	if b == nil || len(asMaps(b["agents"])) != 1 {
		t.Fatal("board lost its agents in trimming")
	}
	// Same payload, but with the board as a bare map: the shape the engine
	// returns through check_in.
	in2 := core.Result{"board": map[string]any{"agents": []any{
		map[string]any{"id": "x", "status": "active"},
	}}}
	if b2 := asMap(panelPayload(in2)["board"]); b2 == nil || len(asMaps(b2["agents"])) != 1 {
		t.Fatal("bare map[string]any board was dropped")
	}
}

// The model-facing summary counts from the FULL result, not the trimmed one,
// otherwise trimming would silently change what the model is told.
func TestSummaryCountsSurviveTrimming(t *testing.T) {
	res := core.Result{
		"board": core.Result{"agents": []core.Result{
			{"id": "a", "status": "active"}, {"id": "b", "status": "dormant"},
		}},
		"inbox": core.Result{"messages": []core.Result{{"serial": 1}, {"serial": 2}}},
	}
	text := showBoardResult(res, false, false)["content"].([]map[string]any)[0]["text"].(string)
	for _, want := range []string{"2 agent(s)", "1 active", "2 unread"} {
		if !strings.Contains(text, want) {
			t.Errorf("summary %q lost %q", text, want)
		}
	}
}

// check_in is the awareness gate: the model reads the board out of its result
// to learn what its peers are doing. An earlier version replaced that JSON with
// a prose summary, which silently broke the thing Dibs exists to do. The
// agent's result must survive the panel being attached.
func TestPanelNeverReplacesTheAgentsResult(t *testing.T) {
	s := &Server{}
	res := core.Result{
		"board":        core.Result{"agents": []core.Result{{"id": "a", "status": "active"}}},
		"inbox":        core.Result{"messages": []core.Result{{"serial": 1}}},
		"acked_serial": 9, "ok": true,
	}
	for _, wantsUI := range []bool{false, true} {
		out := s.panelResult(t.Context(), res, "board", "", wantsUI, false)
		text := out["content"].([]map[string]any)[0]["text"].(string)
		var got map[string]any
		if err := json.Unmarshal([]byte(text), &got); err != nil {
			t.Fatalf("wantsUI=%v: content is not the agent's JSON: %v", wantsUI, err)
		}
		for _, k := range []string{"board", "inbox", "acked_serial", "ok"} {
			if _, ok := got[k]; !ok {
				t.Errorf("wantsUI=%v: agent lost %q from its result", wantsUI, k)
			}
		}
		// The panel copy goes to every client, regardless of what it declared.
		//
		// This assertion was inverted until the reference host disproved it: that
		// host sends `"capabilities":{}` (declaring nothing) and renders the
		// panel anyway from the tool's _meta.ui. Gating on the declaration starved
		// it silently, and a starved panel draws empty, which is indistinguishable
		// from a host bug. Bounded duplication beats a feature that quietly does
		// not work.
		meta, _ := out["_meta"].(map[string]any)
		if _, has := meta[panelDataMetaKey]; !has {
			t.Errorf("wantsUI=%v: no panel payload: hosts render without declaring", wantsUI)
		}
		// structuredContent is the agent's OWN result again: the structured form
		// of content, which is what the field means, and never the panel's
		// trimmed payload.
		//
		// The distinction is the whole point and this check used to miss it by
		// banning key names. A trimmed copy beside content is a second, lesser
		// answer, and a host that shows structuredContent instead of content
		// would hand the agent that one. An identical copy cannot mislead anyone,
		// and it is the only carrier that reaches a panel cached by a client
		// before the content carrier existed.
		structured, ok := out["structuredContent"].(core.Result)
		if !ok {
			t.Errorf("wantsUI=%v: no structuredContent; a panel cached before the "+
				"content carrier has nothing to draw", wantsUI)
			continue
		}
		for k := range got {
			if _, present := structured[k]; !present {
				t.Errorf("wantsUI=%v: structuredContent omits %q that content answers",
					wantsUI, k)
			}
		}
		if len(structured) != len(got) {
			t.Errorf("wantsUI=%v: structuredContent has %d keys, content %d: it must be "+
				"the same answer, not the panel's trimmed payload",
				wantsUI, len(structured), len(got))
		}
	}
}

// A host that drops tool-result _meta must still be able to fill the panel.
//
// This is the test that did not exist when it was needed. The panel's data
// travels in _meta, which is correct and keeps the board out of model context,
// and a host that forwards none of it left the panel on "awaiting board · No
// agents yet" while the daemon held three agents. Nothing failed: `content` was a
// correct 72-character summary the whole time, every assertion about the tool
// result passed, and the only way to see it was to look at the panel.
//
// So the property under test is not "the payload is in _meta": that passed
// throughout, but "the panel has a route to the board that does not depend on
// the host honouring _meta". That route is the bootstrap: a token, and a tool
// the panel can spend it on. Both halves are asserted here, because either one
// alone is again a panel that quietly shows nothing.
func TestPanelCanReachTheBoardOnAHostThatDropsMeta(t *testing.T) {
	srv, _ := newServer(t)
	const marker = "BOARD DETAIL THE BOOTSTRAP MUST NOT CARRY"
	registered := toolCall(t, srv, "register", map[string]any{
		"name": "panel-bootstrap", "description": marker,
	})
	token := registered["token"].(string)

	// What the panel receives when the host forwards content + structuredContent
	// and drops _meta entirely.
	result := rawToolResult(t, srv, "board", map[string]any{"token": token})
	boot, ok := result["structuredContent"].(map[string]any)
	if !ok {
		t.Fatal("no bootstrap: a host that drops _meta leaves the panel with no way to fetch")
	}
	got, _ := boot["act_token"].(string)
	if got != token {
		t.Fatalf("bootstrap act_token = %q, want the caller's own token", got)
	}
	// The token is the caller's own, so it is not new information reaching the
	// model, but the board would be, and that is the cost board promises
	// not to charge.
	blob, _ := json.Marshal(boot)
	if strings.Contains(string(blob), marker) {
		t.Errorf("bootstrap carries board detail: %s", blob)
	}

	// The other half: the call the panel makes with that token has to answer with
	// the board in ordinary content, since content is the one field every host
	// forwards to the app.
	fetched := rawToolResult(t, srv, "board", map[string]any{
		"token": token, "detail": true,
	})
	text, _ := fetched["content"].([]any)[0].(map[string]any)["text"].(string)
	var payload map[string]any
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		t.Fatalf("panel's fetch did not return JSON it can draw: %v", err)
	}
	if _, has := payload["board"]; !has {
		t.Fatal("panel's fetch returned no board; the fallback route is dead")
	}
}

// A panel that has proved it can reach the daemon stops the duplicate.
//
// check_in is called every activation and the board dominates its size, so
// sending it in both content and structuredContent charged the model two copies
// of the fleet per turn. The duplication existed for one host shape: drops
// _meta, forbids app tool calls, shows structuredContent instead of content,
// where structuredContent is the panel's only carrier AND a slim one would
// starve the agent. A panel that has called a tool is proof we are not there.
func TestTheCheckpointIsNotDuplicatedOnceThePanelCanFetch(t *testing.T) {
	s := New(nil)
	res := core.Result{"ok": true, "board": map[string]any{"agents": []any{"a", "b"}}}

	unproved := s.panelResult(t.Context(), res, "board", "", true, false)
	if _, ok := unproved["structuredContent"]; !ok {
		t.Fatal("an unproved session lost structuredContent: on the host this exists " +
			"for, that is the panel's only carrier and the agent's only checkpoint")
	}

	proved := s.panelResult(t.Context(), res, "board", "", true, true)
	if _, ok := proved["structuredContent"]; ok {
		t.Error("the checkpoint is still duplicated after the panel proved it can fetch")
	}
	// The agent must still get everything. Dropping the duplicate is only safe
	// because content was always complete; if that ever stops being true the
	// saving becomes starvation.
	content, _ := proved["content"].([]map[string]any)
	if len(content) == 0 || !strings.Contains(content[0]["text"].(string), "\"board\"") {
		t.Errorf("content no longer carries the board: %v", proved["content"])
	}
	// And the panel keeps its own carrier regardless.
	if _, ok := proved["_meta"]; !ok {
		t.Error("_meta was dropped too: the panel's private space is not the duplicate")
	}
}

// The marker is what the panel actually sends, and nothing else trips it.
func TestOnlyThePanelsOwnMarkerCountsAsAPanelCall(t *testing.T) {
	yes := json.RawMessage(`{"name":"inbox","_meta":{"com.dibs/panel-call":true}}`)
	if !isPanelCall(yes) {
		t.Error("the panel's own marker was not recognised")
	}
	for _, no := range []string{
		`{"name":"inbox"}`,
		`{"name":"inbox","_meta":{}}`,
		`{"name":"inbox","_meta":{"com.dibs/panel-call":false}}`,
		`{"name":"inbox","_meta":{"com.dibs/panel-call":"true"}}`,
		`not json`,
	} {
		if isPanelCall(json.RawMessage(no)) {
			t.Errorf("%s was treated as a panel call: an ordinary agent call must not "+
				"switch off the carrier a panel depends on", no)
		}
	}
}

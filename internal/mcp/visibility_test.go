package mcp

import "testing"

// A registration one agent makes must be visible to the next agent that looks.
//
// codex-primary reported the opposite from a live session: a human told them
// codex-root had joined, their fresh check_in board snapshot did not contain it,
// and the board app did not show it either. By the time the report was read the
// event ring had long since rolled over, so the original cannot be replayed and
// the two plausible causes (their snapshot preceded the registration; the client
// was rendering a panel it had cached for the session, which `dibs doctor`
// warns about by name) cannot be told apart after the fact.
//
// An unreproducible report is still worth resolving, and the way to resolve it
// is to stop it being unreproducible: pin the property so a regression is a
// failing test rather than another agent's afternoon. Measured against a live
// board first, where a fresh registration was present in the very next check_in
// and in the panel payload.
func TestAFreshRegistrationIsVisibleToTheNextAgentThatLooks(t *testing.T) {
	srv, _ := newServer(t)

	watcher := toolCall(t, srv, "register", map[string]any{"name": "watcher"})
	tok, _ := watcher["token"].(string)
	if tok == "" {
		t.Fatal("setup: the watcher got no token")
	}
	before := agentIDs(t, toolCall(t, srv, "check_in", map[string]any{"token": tok}))
	if before["newcomer"] {
		t.Fatal("setup: the newcomer is on the board before it registered")
	}

	if got := toolCall(t, srv, "register", map[string]any{"name": "newcomer"}); got["agent_id"] != "newcomer" {
		t.Fatalf("setup: register returned %v", got)
	}

	// The very next look, by an agent that did nothing in between.
	after := agentIDs(t, toolCall(t, srv, "check_in", map[string]any{"token": tok}))
	if !after["newcomer"] {
		t.Error("an agent that registered is absent from the next check_in board: another " +
			"agent would conclude it is working alone when it is not, which is the one " +
			"thing this board exists to prevent")
	}

	// And the panel the human sees is built from the same snapshot. The report
	// named both surfaces, and they are separate code paths: the board tool
	// renders for the human and answers the model differently.
	panel := toolCall(t, srv, "board", map[string]any{"token": tok, "detail": true})
	if !agentIDs(t, panel)["newcomer"] {
		t.Error("the board panel does not carry an agent that check_in does: the human and " +
			"the agent would be reading different boards")
	}
}

// agentIDs pulls the set of agent ids out of whatever shape a result carries
// its board in, so one test can hold check_in and board to the same standard.
func agentIDs(t *testing.T, res map[string]any) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	var walk func(any)
	walk = func(v any) {
		m, ok := v.(map[string]any)
		if !ok {
			return
		}
		if agents, ok := m["agents"].([]any); ok {
			for _, a := range agents {
				if am, ok := a.(map[string]any); ok {
					if id, ok := am["id"].(string); ok {
						out[id] = true
					}
				}
			}
		}
		for _, nested := range m {
			walk(nested)
		}
	}
	walk(res)
	if len(out) == 0 {
		t.Fatalf("no agents found in the result, so this test can see nothing: %v", res)
	}
	return out
}

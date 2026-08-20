package mcp

import "testing"

// Updating a branch must not erase what the agent is for.
//
// THE LOSS THIS PREVENTS. `update` invites branch-only and title-only calls,
// and the arguments decode into a plain string, so an omitted `description`
// and an explicitly emptied one were the same value by the time the op was
// built. The fold assigns Description unconditionally, correctly, because an op
// already on disk that cleared a description meant to. So every branch-only,
// title-only, model-only or rename-only update silently blanked the one line
// telling everybody what that agent is for, on a board whose whole purpose is
// telling them. Found by a pre-release review.
//
// Fixed at ingress rather than in the fold, and this asserts the thing that
// makes it possible: knowing whether the caller sent the field at all.
func TestAnOmittedDescriptionIsNotAnEmptyOne(t *testing.T) {
	for _, tc := range []struct {
		name   string
		params string
		want   bool
	}{
		{"sent with text", `{"name":"update","arguments":{"description":"reviews PRs"}}`, true},
		// The distinction the decoded struct cannot make.
		{"sent as empty, a deliberate clear", `{"name":"update","arguments":{"description":""}}`, true},
		{"omitted, a branch-only update", `{"name":"update","arguments":{"branch":"main"}}`, false},
		{"no arguments at all", `{"name":"update"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := argumentPresent([]byte(tc.params), "description"); got != tc.want {
				t.Errorf("argumentPresent = %v, want %v. If an omitted field reads as "+
					"present the agent cannot clear its description; if an empty one "+
					"reads as absent, every branch update erases it", got, tc.want)
			}
		})
	}
}

// End to end: a branch-only update leaves the description standing.
func TestABranchOnlyUpdateKeepsTheDescription(t *testing.T) {
	srv, cancel := newServer(t)
	defer cancel()

	reg := callWithIdentity(t, srv, "keeper-nonce",
		`{"name":"keeper","description":"the line that must survive","kind":"persistent"}`)
	tok, _ := reg["token"].(string)
	if tok == "" {
		t.Fatalf("setup: %v", reg)
	}

	callWithIdentityRaw(t, srv, "keeper-nonce", "update", `{"token":"`+tok+`","branch":"feature/x"}`)

	// check_in, because it answers with the board itself. The `board` tool
	// returns a panel-shaped result and the rows sit under _meta, which would
	// make this test about the panel's shape rather than about the description.
	in := callWithIdentityRaw(t, srv, "keeper-nonce", "check_in", `{"token":"`+tok+`"}`)
	board, _ := in["board"].(map[string]any)
	agents, _ := board["agents"].([]any)
	for _, a := range agents {
		m, _ := a.(map[string]any)
		if n, _ := m["name"].(string); n != "keeper" {
			continue
		}
		if d, _ := m["description"].(string); d != "the line that must survive" {
			t.Fatalf("after a branch-only update the description is %q. The board's "+
				"whole job is saying what each agent is for, and updating a branch "+
				"silently emptied it", d)
		}
		return
	}
	t.Fatal("the agent is not on the board")
}

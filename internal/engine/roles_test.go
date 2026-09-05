package engine

import (
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// `[roles]` takes a NAME, and the state is keyed by id.
//
// docs/CONFIGURATION.md documents these values as agent names; register slugs a
// name into an id. The reconciler passed the configured string straight to a
// map lookup, so the documented `admin = ["Fleet Lead"]` waited forever for an
// agent whose id was literally that, while the agent that registered under the
// name was sitting there as `fleet-lead`. Validation accepted it and the daemon
// logged every fifteen seconds that the agent had not registered. Anything with
// a capital, a space, an underscore or a slash was affected, which is most of
// how a person writes a name.
//
// The existing success tests all use already-slugged names, so the distinction
// never appeared: they pass against this.
func TestAConfiguredRoleNameResolvesToTheAgentThatRegisteredIt(t *testing.T) {
	e := &Engine{state: core.NewState("t", core.DefaultLimits())}
	e.state.Agents = map[string]*core.Agent{
		"fleet-lead": {ID: "fleet-lead", Name: "Fleet Lead", Status: core.StatusActive},
		"other":      {ID: "other", Name: "other", Status: core.StatusActive},
	}

	for _, tc := range []struct{ configured, want string }{
		{"Fleet Lead", "fleet-lead"}, // the documented form
		{"fleet-lead", "fleet-lead"}, // the id, which an operator may also write
		{"other", "other"},
	} {
		res := e.resolveConfiguredAgentDecision(tc.configured)
		if got, _ := res["id"].(string); got != tc.want {
			t.Errorf("`[roles]` naming %q resolved to %q, want %q. The reconciler then "+
				"waits for an agent that will never exist, and the operator's declared "+
				"role is never granted", tc.configured, got, tc.want)
		}
	}

	// Not registered yet is the ordinary state of a fresh board, not an error.
	if res := e.resolveConfiguredAgentDecision("nobody"); len(res) != 0 {
		t.Errorf("an unregistered name resolved to %v, want nothing", res)
	}

	// AMBIGUITY IS REFUSED. An id collision is resolved with a suffix, so two
	// agents can share a display name, and picking one would hand a standing
	// role to whichever the map happened to yield first. This is an
	// authorisation path.
	e.state.Agents["fleet-lead-2"] = &core.Agent{
		ID: "fleet-lead-2", Name: "Fleet Lead", Status: core.StatusActive,
	}
	res := e.resolveConfiguredAgentDecision("Fleet Lead")
	if id, _ := res["id"].(string); id != "" {
		t.Errorf("two agents are named `Fleet Lead` and the role resolved to %q. "+
			"Whichever the map yielded first would be granted admin", id)
	}
	if amb, _ := res["ambiguous"].([]string); len(amb) != 2 {
		t.Errorf("ambiguity was not reported: %v", res)
	}

	// A gone agent does not hold a name against a live one.
	delete(e.state.Agents, "fleet-lead-2")
	e.state.Agents["fleet-lead-3"] = &core.Agent{
		ID: "fleet-lead-3", Name: "Fleet Lead", Status: core.StatusClosed,
	}
	if id, _ := e.resolveConfiguredAgentDecision("Fleet Lead")["id"].(string); id != "fleet-lead" {
		t.Errorf("a closed agent sharing the name blocked resolution, giving %q: a "+
			"name is free once its holder is gone, and the declared role would never "+
			"be granted again", id)
	}

	// AND A RETIRED AGENT DOES NOT SHADOW ITS SUCCESSOR THROUGH THE ID BRANCH.
	//
	// A name IS the first agent's id, so when `fleet-lead` retires and a
	// replacement registers under the same name it becomes `fleet-lead-2`. The
	// exact-id branch matched the retired row and returned it, forever: the
	// documented handover resolved the predecessor, the pin then refused, and
	// the board never got the coordinator its config names. The by-name branch
	// had the Gone check and the id branch did not, which is why the case above
	// passes and this one did not exist.
	e.state.Agents = map[string]*core.Agent{
		"fleet-lead":   {ID: "fleet-lead", Name: "fleet-lead", Status: core.StatusArchived},
		"fleet-lead-2": {ID: "fleet-lead-2", Name: "fleet-lead", Status: core.StatusActive},
	}
	if id, _ := e.resolveConfiguredAgentDecision("fleet-lead")["id"].(string); id != "fleet-lead-2" {
		t.Errorf("`[roles]` naming fleet-lead resolved to %q, and that agent is "+
			"archived while its live successor holds the name. The handover the "+
			"operator wrote down can never complete", id)
	}
}

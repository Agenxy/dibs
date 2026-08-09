package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/agenxy/lanes/internal/core"
	"github.com/agenxy/lanes/internal/engine"
	"github.com/agenxy/lanes/internal/ledger"
)

// A role declared in the config is granted at startup — including to a lane that
// has not registered yet.
//
// The ordering is the whole point. A standing coordinator that only took effect
// if it happened to be running when the daemon booted is not standing; and since
// a hand-granted role dies with the ledger it lived in, a board reset would
// silently leave the fleet with nobody able to merge two colliding lanes.
func TestRolesDeclaredInConfigAreGrantedAtStartup(t *testing.T) {
	eng, ctx := testEngine(t)

	// The lanes must exist: a role attaches to a lane, and core answers
	// E_NO_LANE otherwise. That is exactly why the real path re-applies on a
	// ticker instead of granting once at startup.
	registerLane(t, eng, "orchestrator")
	registerLane(t, eng, "fleet-lead")
	applyDeclaredRoles(ctx, eng, RolesConfig{
		Coordinator: []string{"orchestrator"},
		Admin:       []string{"fleet-lead"},
	})

	for lane, want := range map[string]string{
		"orchestrator": core.RoleCoordinator,
		"fleet-lead":   core.RoleAdmin,
	} {
		if !holdsRole(t, eng, lane, want) {
			t.Errorf("%s does not hold %q after applyDeclaredRoles — a role written in "+
				"lanes.toml that does not take effect is a config that lies", lane, want)
		}
	}
}

// An empty [roles] table must grant nothing.
//
// Otherwise the default install quietly hands somebody the god view, which is
// the opposite of the property this whole path is built around.
func TestNoRolesDeclaredGrantsNothing(t *testing.T) {
	eng, ctx := testEngine(t)
	applyDeclaredRoles(ctx, eng, RolesConfig{})
	registerLane(t, eng, "orchestrator")
	if holdsRole(t, eng, "orchestrator", core.RoleCoordinator) {
		t.Error("a lane was granted coordinator from an EMPTY [roles] table; the default " +
			"install must hand nobody breadth it did not ask for")
	}
}

// A name that does not exist, or is empty, must not stop the daemon.
//
// A board is more useful than a perfectly-validated config file: refusing to
// start over one typo leaves the fleet with nowhere to coordinate, and the
// operator unable to read the complaint.
func TestABadRoleEntryDoesNotStopTheDaemon(t *testing.T) {
	eng, ctx := testEngine(t)
	applyDeclaredRoles(ctx, eng, RolesConfig{Coordinator: []string{"", "nobody-here"}})
	// Reaching this line without panicking or hanging is the assertion: a typo
	// in the config must not take the daemon down with it.
}

func registerLane(t *testing.T, eng *engine.Engine, name string) {
	t.Helper()
	if _, err := eng.Do(context.Background(),
		&core.Op{Kind: core.OpRegisterLane, Name: name}); err != nil {
		t.Fatalf("registering %s: %v", name, err)
	}
}

func testEngine(t *testing.T) (*engine.Engine, context.Context) {
	t.Helper()
	dir := t.TempDir()
	box, err := ledger.LoadOrCreateKey(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	led, err := ledger.Open(filepath.Join(dir, "ledger.jsonl"), "test", box)
	if err != nil {
		t.Fatal(err)
	}
	st := core.NewState("test", core.DefaultLimits())
	if _, err := led.Replay(st); err != nil {
		t.Fatal(err)
	}
	eng := engine.New(st, led, nil)
	ctx, cancel := context.WithCancel(context.Background())
	go eng.Run(ctx)
	t.Cleanup(func() { cancel(); _ = led.Close() })
	return eng, ctx
}

// holdsRole asks the state machine rather than parsing a board rendering.
//
// Re-granting a role a lane already holds reports changed:false, which is
// exactly the question "is it already set" — and it does not depend on how the
// board happens to serialise itself today. An earlier version of this helper
// read the board payload and silently returned "" for a role that HAD been
// granted, which made a passing implementation look broken.
func holdsRole(t *testing.T, eng *engine.Engine, lane, role string) bool {
	t.Helper()
	res, err := eng.GrantRole(context.Background(), lane, role)
	if err != nil {
		return false
	}
	changed, _ := res["changed"].(bool)
	return !changed // unchanged ⇒ it already held this role
}

// Config can grant a role. An AGENT still cannot.
//
// This is the property the whole feature is balanced against: the operator's
// file is a human decision, but nothing reachable from an agent may promote
// anything. If grant_role ever became callable with a lane token — directly, or
// by some tool growing a role argument — an agent could hand itself the god view
// and read every other lane's mail.
//
// Asserted here rather than trusted to the HTTP gate, because the gate is one
// layer and this is the invariant underneath it.
func TestAnAgentStillCannotPromoteItself(t *testing.T) {
	eng, _ := testEngine(t)
	reg, err := eng.Do(context.Background(), &core.Op{Kind: core.OpRegisterLane, Name: "climber"})
	if err != nil {
		t.Fatal(err)
	}
	token, _ := reg["token"].(string)
	if token == "" {
		t.Fatal("no token from register_lane; this check would be vacuous")
	}

	// A lane-authenticated grant_role must be refused. The engine treats
	// grant_role as a system op admitted only on the admin path, so presenting a
	// token must not make it work.
	if _, err := eng.Do(context.Background(), &core.Op{
		Kind: core.OpGrantRole, To: "climber", Mode: core.RoleAdmin, Token: token,
	}); err == nil {
		if holdsRole(t, eng, "climber", core.RoleAdmin) {
			t.Fatal("an agent promoted ITSELF to admin using its own lane token — " +
				"that role can read every other lane's mail")
		}
	}

	// And it is not quietly a coordinator either.
	if holdsRole(t, eng, "climber", core.RoleCoordinator) {
		t.Error("an agent acquired coordinator without the admin path")
	}
}

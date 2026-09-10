package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/engine"
)

// Roles declared in the operator's config, applied at startup.
//
// # Why this exists
//
// No agent can promote itself. grant_role is not an MCP tool and is admitted
// only on the daemon's admin path, so an agent asking for a role gets a 401,
// which is the correct answer, because an admin can read every agent's mail and
// an agent that could grant itself that has no boundary left.
//
// But the operator paid for that safety with a chore. Wanting a standing
// coordinator is an ordinary thing, and until now it meant running
// `dibs admin coordinator <agent>` by hand: after every fresh data directory,
// and while typing the admin password. For somebody running several fleets that
// is exactly the mechanical provisioning work that gets skipped, and a
// coordinator nobody remembered to appoint is a fleet with nobody able to merge
// two agents that collided.
//
// A config file is a human decision. The operator owns the file, the daemon
// reads it as itself, and the grant happens on the admin path where it always
// did. Nothing an agent can reach has changed: an agent still cannot promote
// itself, cannot edit this file through Dibs, and cannot ask Dibs to.
//
// # Declared, not remembered
//
// Applied on EVERY start rather than once, because the interesting case is a
// board that was reset. A role granted by hand disappears with the ledger it
// lived in; a role declared in config comes back with the daemon, which is the
// behaviour somebody writing it down expects.
//
//	[roles]
//	coordinator = ["orchestrator"]
//	admin       = ["fleet-lead"]

// keepDeclaredRolesApplied grants the declared roles, and keeps granting them.
//
// A role can only be attached to an agent that EXISTS: core.applyGrantRole
// answers E_NO_AGENT otherwise, and on a fresh daemon no agent exists yet. A
// one-shot grant at startup would therefore do nothing at all on exactly the
// board where the operator most needs it, and would do it silently, which is the
// failure shape this project works hardest to avoid.
//
// So it converges instead: apply now, and again on a slow ticker. An agent that
// registers a minute after the daemon picks up its role a few seconds later,
// without the engine having to know anything about configuration. Re-granting a
// role an agent already holds is free: the state machine reports changed:false
// and ledgers nothing, so the steady-state cost is one map lookup per agent per
// tick.
func keepDeclaredRolesApplied(ctx context.Context, dir string, eng *engine.Engine, c RolesConfig) {
	if len(c.Coordinator) == 0 && len(c.Admin) == 0 {
		return
	}
	pins := loadRolePins(dir)
	applyDeclaredRoles(ctx, eng, c, pins)
	go func() {
		tick := time.NewTicker(rolesReapplyEvery)
		defer tick.Stop()
		// A BOUNDED window, not forever.
		//
		// Retrying indefinitely turned every declared name into a standing
		// invitation: the name sat unclaimed, and whichever agent registered
		// under it at any point later was handed the role. The reason for the
		// retry is narrow and short, an agent starting a few seconds after the
		// daemon, so the window is too. After it closes, a name that never
		// appeared is said out loud once and left alone; restarting the daemon
		// is what re-opens it, which is the moment an operator is present.
		deadline := time.NewTimer(rolesGrantWindow)
		defer deadline.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-deadline.C:
				for role, names := range pins.unclaimed(c) {
					for _, n := range names {
						slog.Warn("declared role was never granted: no agent registered "+
							"under this name while the grant window was open",
							"agent", n, "role", role, "window", rolesGrantWindow,
							"how", "start that agent and restart dibd, or remove the name")
					}
				}
				return
			case <-tick.C:
				applyDeclaredRoles(ctx, eng, c, pins)
			}
		}
	}()
}

// rolesGrantWindow is how long after start a declared name may still be claimed.
//
// Long enough for an operator's own agents to come up behind the daemon, short
// enough that the name is not left open to whoever asks for it first an hour
// later.
const rolesGrantWindow = 2 * time.Minute

// rolesReapplyEvery is how often declared roles are re-checked. Slow on purpose:
// the thing it is waiting for is an agent starting up, which a human notices on
// the scale of seconds, and a tighter loop would buy nothing.
const rolesReapplyEvery = 15 * time.Second

// applyDeclaredRoles grants each declared role once.
func applyDeclaredRoles(ctx context.Context, eng *engine.Engine, c RolesConfig, pins *rolePins) {
	// ADMIN FIRST, AND ONLY WHAT WAS ACTUALLY GRANTED SUPPRESSES A COORDINATOR.
	//
	// One agent holds one role, and a name and an id are two strings for one
	// agent: `coordinator = ["fleet-lead"]` beside `admin = ["Fleet Lead"]`
	// passes validation, which compares strings, and resolves to one identity.
	// Granting both meant coordinator and then admin on every pass: two ledger
	// entries every fifteen seconds and a window in between where admin-only
	// calls fail.
	//
	// The first fix for that collected every admin alias that RESOLVED and
	// skipped a coordinator naming the same agent. Resolving is not being
	// authorised: with the admin alias missing from `[roles.identity]`, the
	// coordinator grant was skipped for an admin grant that was then refused,
	// so nothing was granted at all, and the launch claim stays suppressed
	// because the config did name a coordinator. A fresh board came up with no
	// coordinator and no way to get one, which is the state the claim exists to
	// prevent.
	//
	// So admin runs first and reports what it actually did. Only an agent
	// HOLDING admin suppresses its own coordinator declaration, and admin
	// already includes coordinator authority, so nothing is lost either way.
	held := map[string]bool{}
	for _, agent := range c.Admin {
		if id := grantOne(ctx, eng, c, pins, core.RoleAdmin, agent); id != "" {
			held[id] = true
		}
	}
	for _, agent := range c.Coordinator {
		id := resolveDeclared(ctx, eng, core.RoleCoordinator, agent)
		if id == "" {
			continue
		}
		if held[id] {
			slog.Info("an agent is declared as both coordinator and admin under "+
				"different spellings; it holds admin, which includes what coordinator "+
				"can do", "agent", agent, "id", id)
			continue
		}
		if !mayHoldDeclaredRole(ctx, eng, pins, c, core.RoleCoordinator, agent, id) {
			continue
		}
		grantDeclared(ctx, eng, core.RoleCoordinator, agent, id)
	}
	withdrawUndeclaredRoles(ctx, eng, c, pins)
}

// withdrawUndeclaredRoles takes back what this mechanism granted and the
// config no longer authorises. Issue #73.
//
// `[roles]` decided what got GRANTED and never what got withdrawn. A role is
// replayable state, so an operator who deleted a name from the file watched
// the ledger restore the agent as admin on the next boot, and the reconciler
// simply declined to grant it again: the god view over every mailbox, held
// by an agent the config no longer named, until somebody ran `dibs admin
// member` by hand.
//
// WITHDRAW ONLY WHAT THIS MECHANISM GRANTED AND CAN STILL PROVE IT GRANTED.
// The pin file is that proof: it records the credential each declared role
// went to. Every rule wider than this has a bad edge. Demoting on any refusal
// would strip a board mid-edit of its admin inside fifteen seconds, and a
// v0.0.6 board upgrading with no [roles.identity] yet on first boot. Demoting
// whatever holds a declared role would take a role a person granted by hand,
// or one an agent took through the launch claim, neither of which this
// mechanism gave and neither of which it should touch.
//
// So, for each pinned (role, name, fingerprint): if the config still declares
// that name for that role AND still authorises that fingerprint, nothing to
// do. Otherwise the grant is withdrawn from the agent that holds it, but only
// when that agent's fingerprint IS the pinned one, and the pin is dropped
// either way. A pin whose fingerprint no longer matches the holder is stale,
// which means this mechanism did not grant what that agent holds; dropping it
// is right and demoting would be wrong.
//
// A person's decision still stands: the engine records a hand-made role
// change for the run and declines the reconciler's grant, and it declines its
// withdrawal the same way.
func withdrawUndeclaredRoles(ctx context.Context, eng *engine.Engine, c RolesConfig, pins *rolePins) {
	if pins.Pins == nil {
		return // unreadable: fail in the refusing direction, as check does
	}
	declared := map[string]map[string]bool{
		core.RoleAdmin:       {},
		core.RoleCoordinator: {},
	}
	for _, n := range c.Admin {
		declared[core.RoleAdmin][n] = true
	}
	for _, n := range c.Coordinator {
		declared[core.RoleCoordinator][n] = true
	}
	changed := false
	for role, byName := range pins.Pins {
		for name, pinned := range byName {
			want := c.Identity[name]
			if declared[role][name] && (want == "" || want == pinned) {
				continue // still declared, still this credential
			}
			delete(byName, name)
			changed = true
			withdrawOne(ctx, eng, role, name, pinned)
		}
	}
	if changed {
		if err := pins.save(); err != nil {
			slog.Warn("could not save the role pins after a withdrawal", "err", err)
		}
	}
}

// withdrawOne demotes the holder of a declared name, if it is the agent the
// pin recorded and it still holds the pinned role.
func withdrawOne(ctx context.Context, eng *engine.Engine, role, name, pinned string) {
	id := resolveDeclared(ctx, eng, role, name)
	if id == "" {
		return // nobody holds the name; the pin was all there was
	}
	fp, err := eng.AgentIdentity(ctx, id)
	if err != nil || fp != pinned {
		slog.Info("a declared role's pin is stale: a different agent holds the "+
			"name, so the pin is dropped and the role left alone",
			"agent", name, "role", role)
		return
	}
	if held, err := eng.AgentRole(ctx, id); err != nil || held != role {
		return // already not holding what was pinned
	}
	res, err := eng.GrantRole(ctx, id, core.RoleMember)
	if err != nil {
		slog.Warn("could not withdraw a role no longer declared in dibs.toml",
			"agent", name, "role", role, "err", err)
		return
	}
	if res["stands"] != nil {
		slog.Info("declared role not withdrawn: a person set this agent's role "+
			"during this run", "agent", name, "role", role, "why", res["stands"])
		return
	}
	slog.Info("withdrew a role no longer declared in dibs.toml", "agent", name, "role", role)
}

// grantOne resolves a declared name, grants the role if it may, and reports the
// agent id when the role is now held. "" means nothing was granted, for any
// reason: not registered, not authorised, or refused by the pin.
func grantOne(ctx context.Context, eng *engine.Engine, c RolesConfig, pins *rolePins,
	role, agent string,
) string {
	if agent == "" {
		return ""
	}
	id := resolveDeclared(ctx, eng, role, agent)
	if id == "" {
		return ""
	}
	if !mayHoldDeclaredRole(ctx, eng, pins, c, role, agent, id) {
		return ""
	}
	grantDeclared(ctx, eng, role, agent, id)
	return id
}

// describeDeclaredRoles renders the roles for the startup banner, so an operator
// can see what the config actually did without reading the ledger.
func describeDeclaredRoles(c RolesConfig) string {
	if len(c.Coordinator) == 0 && len(c.Admin) == 0 {
		return ""
	}
	return fmt.Sprintf("coordinator=%v admin=%v", c.Coordinator, c.Admin)
}

// mayHoldDeclaredRole answers WHICH agent, not which name.
//
// The config authorises a string, and a name is free for anyone to take once
// its holder is gone. Pinning the credential of the agent that first receives
// the role is what makes a standing role follow an identity, and refusing an
// agent with no nonce is what stops it being pinned to something that cannot
// prove itself tomorrow.
// grantDeclared performs one declared grant and says what happened.
//
// Split out for the same reason resolveDeclared is: this loop hands out
// standing privilege, and a reader has to be able to hold all of it at once.
func grantDeclared(ctx context.Context, eng *engine.Engine, role, agent, id string) {
	res, err := eng.GrantRole(ctx, id, role)
	if err == nil && res["stands"] != nil {
		// The engine declined: a person changed this agent's role during
		// this run, on the same loop that would have applied the regrant.
		slog.Info("declared role not reapplied", "agent", agent, "role", role, "why", res["stands"])
		return
	}
	if err != nil {
		// An agent that has not registered yet is the NORMAL case on a fresh
		// board, not a misconfiguration, so it is logged at debug and retried on
		// the next tick. Anything else is worth seeing.
		var cerr *core.Error
		if errors.As(err, &cerr) && cerr.Code == "E_NO_AGENT" {
			slog.Debug("declared role is waiting for its agent to register",
				"agent", agent, "role", role)
			return
		}
		// Never fatal: a daemon that refuses to start over one wrong name in a
		// config file leaves the fleet with nowhere to coordinate and the
		// operator unable to read the complaint.
		slog.Warn("could not grant a role declared in dibs.toml",
			"agent", agent, "role", role, "err", err)
		return
	}
	// Only announce a real change, or the log fills with the same line every
	// fifteen seconds forever.
	if changed, _ := res["changed"].(bool); changed {
		slog.Info("granted a role declared in dibs.toml", "agent", agent, "role", role)
	}
}

// tomlKey renders a key the way the operator has to type it.
//
// THE ONE INSTRUCTION THIS FEATURE HAS, and it emitted invalid TOML for exactly
// the names the reconciler had just learned to accept. A bare key may hold only
// letters, digits, underscores and dashes, so `Fleet Lead = "..."` does not
// parse: an operator following the daemon's own advice got a dibs.toml the
// daemon then refuses to load, and the role they were trying to grant stayed
// ungranted with a new fault on top. Quoted whenever it has to be, and left
// bare when it does not, because an unnecessary quote invites the reader to
// copy it into places where it is wrong.
func tomlKey(s string) string {
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '_', r == '-':
		default:
			return strconv.Quote(s)
		}
	}
	if s == "" {
		return `""`
	}
	return s
}

// resolveDeclared turns what the operator wrote in `[roles]` into an agent id,
// or "" when there is nothing to do this tick.
//
// THE CONFIG NAMES AN AGENT; THE ENGINE IS KEYED BY ID. The configured string
// used to go straight through, so the documented `admin = ["Fleet Lead"]`
// waited forever for an agent whose id was literally that, while the agent that
// registered under the name sat there as `fleet-lead`.
//
// Split out because applyDeclaredRoles is already at the complexity limit, and
// the reason it is worth keeping under one is that this loop grants standing
// privilege: a reader has to be able to hold all of it at once.
func resolveDeclared(ctx context.Context, eng *engine.Engine, role, agent string) string {
	id, err := eng.ResolveConfiguredAgent(ctx, agent)
	if err == nil {
		return id
	}
	// Not registered yet is the NORMAL case on a fresh board: the daemon comes
	// up before its agents do, and the reconciler runs on a ticker. Anything
	// else means the config names something that cannot be resolved, which a
	// person has to fix and should therefore be able to see.
	var cerr *core.Error
	if errors.As(err, &cerr) && cerr.Code == "E_NO_AGENT" {
		slog.Debug("declared role is waiting for its agent to register",
			"agent", agent, "role", role)
	} else {
		slog.Warn("declared role names no single agent",
			"agent", agent, "role", role, "err", err)
	}
	return ""
}

// agent is what the operator wrote, and keys the pin file and [roles.identity];
// id is the agent it resolved to, and is what the engine is asked about.
func mayHoldDeclaredRole(ctx context.Context, eng *engine.Engine, pins *rolePins,
	c RolesConfig, role, agent, id string,
) bool {
	fp, err := eng.AgentIdentity(ctx, id)
	if err != nil {
		// Not registered yet is the ordinary case while the window is open.
		slog.Debug("declared role is waiting for its agent to register",
			"agent", agent, "role", role)
		return false
	}
	// The config already holds the FINGERPRINT, so nothing is hashed here. It
	// used to hold the nonce, which is the agent's whole recovery credential
	// and made dibs.toml a file that hands the admin identity to anything
	// running as the operator.
	if err := pins.check(role, agent, fp, c.Identity[agent]); err != nil {
		slog.Error("refusing to grant a role declared in dibs.toml",
			"agent", agent, "role", role, "err", err)
		// The operator cannot look this up anywhere else, and printing it is
		// safe: it is a hash of the nonce and reveals nothing that could be
		// replayed. Without this the feature has a bootstrap step with no way
		// to complete it.
		if c.Identity[agent] == "" {
			slog.Warn("to grant it, pin this agent's identity in dibs.toml",
				"agent", agent, "role", role,
				"add", fmt.Sprintf("[roles.identity]\n%s = %q", tomlKey(agent), fp))
		}
		return false
	}
	return true
}

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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
	for _, spec := range []struct {
		role   string
		agents []string
	}{
		{core.RoleCoordinator, c.Coordinator},
		{core.RoleAdmin, c.Admin},
	} {
		for _, agent := range spec.agents {
			if agent == "" {
				continue
			}
			if !mayHoldDeclaredRole(ctx, eng, pins, c, spec.role, agent) {
				continue
			}
			res, err := eng.GrantRole(ctx, agent, spec.role)
			if err != nil {
				// An agent that has not registered yet is the NORMAL case on a
				// fresh board, not a misconfiguration, so it is logged at debug
				// and retried on the next tick. Anything else is worth seeing.
				var cerr *core.Error
				if errors.As(err, &cerr) && cerr.Code == "E_NO_AGENT" {
					slog.Debug("declared role is waiting for its agent to register",
						"agent", agent, "role", spec.role)
					continue
				}
				// Never fatal: a daemon that refuses to start over one wrong name
				// in a config file leaves the fleet with nowhere to coordinate
				// and the operator unable to read the complaint.
				slog.Warn("could not grant a role declared in dibs.toml",
					"agent", agent, "role", spec.role, "err", err)
				continue
			}
			// Only announce a real change, or the log fills with the same line
			// every fifteen seconds forever.
			if changed, _ := res["changed"].(bool); changed {
				slog.Info("granted a role declared in dibs.toml",
					"agent", agent, "role", spec.role)
			}
		}
	}
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
func mayHoldDeclaredRole(ctx context.Context, eng *engine.Engine, pins *rolePins,
	c RolesConfig, role, agent string,
) bool {
	fp, err := eng.AgentIdentity(ctx, agent)
	if err != nil {
		// Not registered yet is the ordinary case while the window is open.
		slog.Debug("declared role is waiting for its agent to register",
			"agent", agent, "role", role)
		return false
	}
	if err := pins.check(role, agent, fp, engine.RolePinFingerprint(c.Identity[agent])); err != nil {
		slog.Error("refusing to grant a role declared in dibs.toml",
			"agent", agent, "role", role, "err", err)
		return false
	}
	return true
}

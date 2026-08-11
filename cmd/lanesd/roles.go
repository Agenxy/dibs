package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/agenxy/lanes/internal/core"
	"github.com/agenxy/lanes/internal/engine"
)

// Roles declared in the operator's config, applied at startup.
//
// # Why this exists
//
// No agent can promote itself. grant_role is not an MCP tool and is admitted
// only on the daemon's admin path, so an agent asking for a role gets a 401,
// which is the correct answer, because an admin can read every lane's mail and
// an agent that could grant itself that has no boundary left.
//
// But the operator paid for that safety with a chore. Wanting a standing
// coordinator is an ordinary thing, and until now it meant running
// `lanes admin coordinator <lane>` by hand: after every fresh data directory,
// and while typing the admin password. For somebody running several fleets that
// is exactly the mechanical provisioning work that gets skipped, and a
// coordinator nobody remembered to appoint is a fleet with nobody able to merge
// two lanes that collided.
//
// A config file is a human decision. The operator owns the file, the daemon
// reads it as itself, and the grant happens on the admin path where it always
// did. Nothing an agent can reach has changed: an agent still cannot promote
// itself, cannot edit this file through Lanes, and cannot ask Lanes to.
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

// RolesConfig is the [roles] table.
type RolesConfig struct {
	// Coordinator gets breadth without intrusion: broadcast, force-release,
	// merge and evict, but never another lane's mail.
	Coordinator []string `toml:"coordinator"`
	// Admin adds the god view, mail included. Grant it only to an agent trusted
	// as the operator trusts themselves.
	Admin []string `toml:"admin"`
}

// keepDeclaredRolesApplied grants the declared roles, and keeps granting them.
//
// A role can only be attached to a lane that EXISTS: core.applyGrantRole
// answers E_NO_LANE otherwise, and on a fresh daemon no lane exists yet. A
// one-shot grant at startup would therefore do nothing at all on exactly the
// board where the operator most needs it, and would do it silently, which is the
// failure shape this project works hardest to avoid.
//
// So it converges instead: apply now, and again on a slow ticker. A lane that
// registers a minute after the daemon picks up its role a few seconds later,
// without the engine having to know anything about configuration. Re-granting a
// role a lane already holds is free: the state machine reports changed:false
// and ledgers nothing, so the steady-state cost is one map lookup per lane per
// tick.
func keepDeclaredRolesApplied(ctx context.Context, eng *engine.Engine, c RolesConfig) {
	if len(c.Coordinator) == 0 && len(c.Admin) == 0 {
		return
	}
	applyDeclaredRoles(ctx, eng, c)
	go func() {
		tick := time.NewTicker(rolesReapplyEvery)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				applyDeclaredRoles(ctx, eng, c)
			}
		}
	}()
}

// rolesReapplyEvery is how often declared roles are re-checked. Slow on purpose:
// the thing it is waiting for is an agent starting up, which a human notices on
// the scale of seconds, and a tighter loop would buy nothing.
const rolesReapplyEvery = 15 * time.Second

// applyDeclaredRoles grants each declared role once.
func applyDeclaredRoles(ctx context.Context, eng *engine.Engine, c RolesConfig) {
	for _, spec := range []struct {
		role  string
		lanes []string
	}{
		{core.RoleCoordinator, c.Coordinator},
		{core.RoleAdmin, c.Admin},
	} {
		for _, lane := range spec.lanes {
			if lane == "" {
				continue
			}
			res, err := eng.GrantRole(ctx, lane, spec.role)
			if err != nil {
				// A lane that has not registered yet is the NORMAL case on a
				// fresh board, not a misconfiguration, so it is logged at debug
				// and retried on the next tick. Anything else is worth seeing.
				var cerr *core.Error
				if errors.As(err, &cerr) && cerr.Code == "E_NO_LANE" {
					slog.Debug("declared role is waiting for its lane to register",
						"lane", lane, "role", spec.role)
					continue
				}
				// Never fatal: a daemon that refuses to start over one wrong name
				// in a config file leaves the fleet with nowhere to coordinate
				// and the operator unable to read the complaint.
				slog.Warn("could not grant a role declared in lanes.toml",
					"lane", lane, "role", spec.role, "err", err)
				continue
			}
			// Only announce a real change, or the log fills with the same line
			// every fifteen seconds forever.
			if changed, _ := res["changed"].(bool); changed {
				slog.Info("granted a role declared in lanes.toml",
					"lane", lane, "role", spec.role)
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

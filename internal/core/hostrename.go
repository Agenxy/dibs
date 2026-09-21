package core

import "time"

// applyHostRenamed rewrites every row and claim that carries the id this
// computer used to answer to.
//
// The identity a machine is known by can change once: when a machine that
// already ran Dibs joins the fleet and the daemon there next starts. Rows
// registered before that, and the claims they hold, carry the old id, and
// the fold compares those strings: the machine's own agents read as remote
// (so it refuses its own wake commands for them) and two agents on it can
// each take an exclusive claim on the same path outside a checkout,
// because each looks like somebody else's machine. The ingress rewrites
// what ARRIVES (Engine.SetHostAliases); this rewrites what is already
// here.
//
// Both ids are on the op, so replay makes the same substitution whatever
// the daemon's identity is at the time. Nothing else about a row changes,
// and an op naming an id no row carries is a no-op that advances no
// serial: a daemon restarting under an identity it already had ledgers
// nothing. Round thirty-six of the pre-release review.
func (s *State) applyHostRenamed(op *Op, now time.Time) (Result, []Event, error) {
	was, now_ := op.HostWas, op.HostNow
	if was == "" || now_ == "" || was == now_ {
		return Result{"ok": true, "changed": 0}, nil, nil
	}
	changed := 0
	for _, l := range s.Agents {
		if l.Agent != nil && l.Agent.HostID == was {
			l.Agent.HostID = now_
			changed++
		}
	}
	claims := 0
	for i := range s.Claims {
		moved := false
		if s.Claims[i].Host == was {
			s.Claims[i].Host = now_
			moved = true
		}
		// AND THE REPOSITORY SNAPSHOT THE CLAIM CARRIES. Two linked
		// worktrees of one checkout are recognised as one tree by the git
		// common directory they share, and that evidence is accepted only
		// between snapshots that speak for the same machine
		// (sameRepoIdentity). Renaming the row and the claim and leaving
		// the snapshot behind made a claim held before the adoption read
		// as another computer's tree, so a conflicting write from the
		// other worktree of the same checkout stopped colliding with it:
		// the paths differ there, so nothing else catches it. Round
		// thirty-nine of the pre-release review.
		if r := s.Claims[i].Repo; r != nil && r.HostID == was {
			r.HostID = now_
			moved = true
		}
		if moved {
			claims++
		}
	}
	if changed == 0 && claims == 0 {
		return Result{"ok": true, "changed": 0}, nil, nil
	}
	evs := []Event{{Type: "host.renamed", Data: map[string]any{
		"was": was, "now": now_, "agents": changed, "claims": claims,
	}}}
	s.finish(&evs, now)
	return Result{"ok": true, "changed": changed, "claims": claims}, evs, nil
}

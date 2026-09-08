package core

import (
	"slices"
	"time"
)

// The coordinator seat and the prune it authorises. Split from apply.go when
// that file passed the length limit; nothing here changed in the move.

// applyPrune closes agents the human has finished with. Reaching a dead agent is
// otherwise impossible: sign_off needs the agent's own token, and an agent that
// crashed or lost its context no longer has one, so without this the board
// accumulates debris nobody can clear.
//
// op.To names a single agent; empty means "every agent that is not live", which is
// the common case after a day's work.
// applyClaimCoordinator promotes the agent that started this daemon.
//
// Roles were human-only, which left a fleet with no human at the keyboard
// unable to ever have a coordinator: force_release, close_space and clearing
// another agent's debris were permanently unreachable. That is a poor fit for a
// tool whose claim is that agents drive it.
//
// The claim is not a security boundary and does not pretend to be one. Every
// agent already shares one coordination secret, so agent-to-agent isolation is
// "a bar to raise, not a wall" (SECURITY.md), and an agent that can reach the
// daemon can already impersonate any other. What the claim buys is
// DELIBERATENESS: the role is taken by an explicit act, once, by something that
// could read the daemon's own data directory, rather than assumed.
//
// Persistent only, so the role is durable by construction. An ephemeral agent
// that claimed and then signed off would take the role into a closed record and
// leave the board with no coordinator and no claim left to make.
func (s *State) applyClaimCoordinator(op *Op, actor *Agent, now time.Time) (Result, []Event, error) {
	if !op.ClaimVerified {
		return nil, nil, errf("E_BAD_CLAIM",
			"the claim secret is in `coordinator.claim` in the daemon's data directory, "+
				"readable by whoever started it. It is consumed by the first successful claim",
			"coordinator claim rejected")
	}
	if actor.Kind != KindPersistent {
		return nil, nil, errf("E_NOT_PERSISTENT",
			"register with kind \"persistent\" and a nonce first: the role has to outlive "+
				"this process, and an ephemeral record takes it away when it signs off",
			"agent %s is ephemeral", actor.ID)
	}
	actor.Role = RoleCoordinator
	// LEDGERED, via finish. This reaches Apply through the actor-op switch and
	// so escapes the finishing path ordinary ops take, which is the same escape
	// applyPrune documents above and the same bug: without it the serial never
	// moves, the engine never appends, and replay undoes the grant. Measured
	// end to end before it was caught here: the claim returned role=coordinator,
	// the daemon restarted, and the agent was a member again with the claim
	// re-minted, because the board had no memory of ever settling the question.
	evs := []Event{{
		Type: "agent.role", Agent: actor.ID,
		Data: map[string]any{"role": RoleCoordinator, "via": "launch claim"},
	}}
	// The actor's durable checkpoint, which the common path sets and this one
	// returns before reaching. Same escape as the missing finish() above, and
	// the same one adoption already carries a paragraph about: the engine's
	// derived `seen` map hides it while the daemon runs and is deliberately not
	// replayable, so after a restart an agent is judged against the checkpoint
	// it had BEFORE the op, and one that has just claimed coordinator can be
	// swept stale immediately. Found by the pre-release review.
	//
	// GATED. This refresh is v0.0.7 behaviour in the fold. A v0.0.6 ledger
	// holds a claim, then a same-nonce register that reattached and rotated
	// its token because the checkpoint was stale; replaying the claim with the
	// refresh made that register take the "still active" shortcut instead,
	// keeping the old token and allocating no serial. Every serial after it
	// then disagrees with the ledger. Found by the pre-release review, round
	// four.
	if op.V7Semantics {
		actor.LastCoordination = now
	}
	serial := s.finish(&evs, now)
	return Result{"ok": true, "agent_id": actor.ID, "role": RoleCoordinator, "serial": serial}, evs, nil
}

// applyPruneOwn lets an agent remove a record it is responsible for.
//
// Itself, or a child it VOUCHED for. Never a peer, and that restriction is the
// whole point rather than caution: an agent able to prune peers can delete the
// row that would have told it somebody else is already doing its work, which is
// the alarm this system exists to raise, switched off from the inside. Vouching
// is what makes a parent accountable for a child (SPEC-CHANNELS §8.2), so it is
// also what entitles the parent to clean up after it.
//
// Only finished agents. Pruning a working agent would release its claims and
// blank its token underneath it, which is coercion; sign_off is how an agent
// stops, and this is how the record is tidied afterwards.
func (s *State) applyPruneOwn(op *Op, actor *Agent, now time.Time) (Result, []Event, error) {
	target := s.Agents[op.To]
	if target == nil {
		return nil, nil, errf("E_NO_AGENT", "check the id on the board", "no agent %q", op.To)
	}
	// The coordinator is the one agent that may tidy somebody else's record, and
	// only debris: the active check below still applies to it, unconditionally.
	//
	// That split is the whole design. An agent that can prune a LIVE peer can
	// delete the row that would have told it somebody else is already pursuing
	// its objective, which is the single thing this board exists to show, so no
	// role gets that. A record whose agent has stopped shows nothing and blocks
	// the tidying that the role was created for: a fleet with nobody at the
	// keyboard could otherwise never clear debris at all.
	mine := target.ID == actor.ID ||
		(target.Parent == actor.ID && target.ParentProven)
	if !mine && !actor.IsCoordinator() {
		return nil, nil, errf("E_NOT_YOURS",
			"you can prune your own record and children you vouched for. Ask the "+
				"coordinator, or a human (`dibs admin prune`), to remove somebody else's",
			"agent %q is not yours to prune", target.ID)
	}
	if target.Status == StatusActive {
		return nil, nil, errf("E_AGENT_ACTIVE",
			"let it finish, or sign_off first: pruning a working agent would release "+
				"its claims underneath it",
			"agent %q is still active", target.ID)
	}
	// LEDGERED, for the reason spelled out on applyPrune above, which this
	// managed to reproduce anyway: closing an agent blanks its token and
	// releases its claims, and without finish() the serial never moves, so the
	// engine never appends and replay undoes all of it. The caller is told the
	// prune succeeded and the record is back after the next restart, stale
	// rather than closed, holding its old token again.
	//
	// Watched happen on a real board: two dead probes pruned, gone from the
	// board, and back three minutes later when the daemon restarted. The five
	// tests below were green throughout, because in-process state is exactly
	// what a prune with no ledger record gets right.
	// ALREADY CLOSED IS NOT A TRANSITION, and the admin path beside this one
	// learned that a round ago while this did not.
	//
	// The active check above rejects a working agent; a CLOSED one fell straight
	// through to applyClose, which emits agent.closed unconditionally, and to
	// finish, which advances the serial. So pruning a closed record wrote down a
	// close that had already happened and moved the serial for no change to
	// replayable state: the audit stream then claims a transition that never
	// occurred, and `dibs log` cannot tell it from a real one.
	//
	// Gated on V7Semantics for the same reason as the sibling repair: a v0.0.6
	// prune really did emit that event and really did advance, and replay has to
	// reach the board that existed. Found by the pre-release review, which noted
	// the changelog overstated the repair by describing only the admin half.
	if op.V7Semantics && target.Status == StatusClosed {
		// CHANGED: FALSE, because the repair stopped at the ledger and left the
		// answer saying a prune happened. The admin path beside this one
		// truthfully returns an empty list and count 0; this returned
		// {"ok":true,"pruned":<id>}, which reads as a prune to anything that
		// reads results, and its own regression test discarded the result and
		// so never saw it. Found by the pre-release review.
		return Result{
			"ok": true, "pruned": nil, "changed": false, "serial": s.Serial,
			"note": "agent " + target.ID + " was already closed: nothing to prune, " +
				"and nothing was recorded",
		}, nil, nil
	}
	// The actor's durable checkpoint. See applyClaimCoordinator: this returns
	// out of the dispatcher too, so it misses the common path's assignment.
	// Gated for the same reason as that one.
	if op.V7Semantics {
		actor.LastCoordination = now
	}
	_, evs := s.applyClose(target, now)
	serial := s.finish(&evs, now)
	return Result{"ok": true, "pruned": target.ID, "changed": true, "serial": serial}, evs, nil
}

func (s *State) applyPrune(op *Op, now time.Time) (Result, []Event, error) {
	var targets []*Agent
	if op.To != "" {
		l := s.Agents[op.To]
		if l == nil {
			return nil, nil, errf("E_NO_AGENT", "check the id on the board", "no agent %q", op.To)
		}
		targets = append(targets, l)
	} else {
		// Sorted, because the events below go into the ledger in this order.
		// Ranging the map directly gave a different audit sequence every run.
		for _, id := range sortedKeys(s.Agents) {
			l := s.Agents[id]
			// Never prune an agent that is still working: only the debris.
			if l.Status != StatusActive && l.Status != StatusClosed {
				targets = append(targets, l)
			}
		}
	}
	var evs []Event
	var ids []string
	for _, l := range targets {
		// ALREADY CLOSED IS NOT A TRANSITION.
		//
		// The all-agent branch skips closed agents; the named branch did not, so
		// pruning one twice ran applyClose again and emitted a second
		// `agent.closed` for an agent that closed once. The audit stream is the
		// thing `dibs log` and every events_since consumer reads, and a
		// transition that never happened is worse there than a missing one,
		// because it is indistinguishable from a real one. Found by the
		// pre-release review.
		if op.V7Semantics && l.Status == StatusClosed {
			continue
		}
		r, e := s.applyClose(l, now)
		_ = r
		evs = append(evs, e...)
		ids = append(ids, l.ID)
	}
	slices.Sort(ids)
	// NOTHING CLOSED MEANS NOTHING TO WRITE DOWN.
	//
	// This called finish unconditionally, so a prune that found no debris
	// advanced the serial and the engine appended an op recording that nothing
	// happened. "An op is ledgered iff it changed replayable state" is the rule
	// this repository states about itself, and an idle prune broke it in the
	// direction that grows the ledger forever.
	//
	// Worth being explicit about the replay consequence: a historical empty
	// prune DID advance the serial, so replaying one now leaves the state one
	// behind the ledger's own number. That is the tolerated case, not the fatal
	// one: Replay resyncs forward on a gap and only refuses when state runs
	// AHEAD. Choosing that over an ever-growing ledger of no-ops is deliberate.
	if op.V7Semantics && len(ids) == 0 {
		return Result{"ok": true, "pruned": ids, "count": 0, "serial": s.Serial}, nil, nil
	}
	// LEDGERED. applyPrune closes agents, blanks their tokens and releases their
	// claims, and it returned without finish(), so the serial never moved, the
	// engine never appended, and replay undid all of it. The human was told the
	// prune succeeded; after the next restart the agents were back, stale rather
	// than closed, holding their old tokens again.
	//
	// It reaches this point through the special-op switch, which is why it
	// escaped the finishing path every ordinary op goes through.
	serial := s.finish(&evs, now)
	return Result{"ok": true, "pruned": ids, "count": len(ids), "serial": serial}, evs, nil
}

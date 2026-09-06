package core

import "time"

// resumeLiveAgent answers a register whose nonce matched an agent that is still
// active and was last heard from inside one TTL.
//
// Extracted from applyRegister because it is a decision with two cases that
// look identical from outside and are not, and because the file it lived in had
// reached the length where nobody reads the branch they are not looking for.
func (s *State) resumeLiveAgent(l *Agent, op *Op, now time.Time) (Result, []Event) {
	// A RESUME MAY BE CARRYING A THREAD ID NOBODY HAD.
	//
	// This branch was written as a response-loss retry: the same nonce arriving
	// twice inside one TTL is the client repeating a call whose answer it never
	// saw, so it returns the original result and changes nothing. Correct for
	// that case.
	//
	// It is not the only case that lands here. An agent that is still active and
	// re-registers at the start of an activation, which is what `dibs://skills`
	// tells every agent to do, comes back `resumed` too, and it may be doing so
	// from a session the board has never seen. Codex puts `threadId` in `_meta`
	// on every call and that id is the one `codex exec resume` takes, so this is
	// exactly where a returning agent hands over the only thing that makes it
	// wakeable. Dropping it left the agent reachable only while it kept making
	// other calls: register, then stop, and nothing could start it again.
	//
	// Measured on this board: fifteen of twenty-nine persistent agents had a
	// wake command for their harness and no thread for it to name, including one
	// that had registered that morning.
	//
	// GATED ON THE RECORDED DECISION, and on the alias being NEW. Binding is a
	// replayable state change, so it has to advance the serial and be ledgered
	// (SPEC §2); doing that unconditionally would make replay of a v0.0.6 ledger
	// advance the serial where the original fold did not, and every serial after
	// it would disagree with what the ledger records. Same hazard, same gate, as
	// the two repairs V7Semantics already covers.
	// NEW, OR NO LONGER A GUESS. Binding only a new alias left one the daemon
	// had inferred marked as guessed after its owner named it outright, so a
	// stranger could still reclaim it. bindHarnessSessionAs upgrades the
	// provenance; the gate has to let it run for that case too. Found by the
	// pre-release review, round two.
	alias := op.SessionAlias
	// A return to a thread bound earlier is a change: nothing in the sets
	// moves, and the activation to wake does. Found by the pre-release review,
	// round eight.
	changed := alias != "" && (!l.holdsSession(alias) || l.GuessedSession(alias) || l.CurrentSession != alias)
	if op.V7Semantics && changed {
		s.dropTakenSession(op, l)
		l.bindHarnessSessionAs(op.SessionAlias, op.SessionGuessed)
		// A LEDGERED activation is durable evidence of life. Without this the
		// engine touched only its transient seen map, and a restart just past
		// the old TTL booted the agent stale despite a registration on disk
		// seconds old. Found by the pre-release review, round three.
		l.LastCoordination = now
		evs := []Event{{Type: "agent.resumed", Agent: l.ID, Data: map[string]any{
			"via": "nonce", "session_bound": true,
		}}}
		serial := s.finish(&evs, now)
		return Result{
			"agent_id": l.ID, "token": l.Token, "serial": serial,
			"resumed": true, "board": s.Board(),
		}, evs
	}
	return Result{
		"agent_id": l.ID, "token": l.Token, "serial": s.Serial,
		"resumed": true, "board": s.Board(),
	}, nil
}

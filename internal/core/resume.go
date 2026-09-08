package core

import "time"

// resumeWork is what a live resume would CHANGE, and whether it owes the row
// its nonce back. Split from resumeLiveAgent because that function had grown
// past what one reader can hold: this half is a list of independent reasons a
// resume is not the response-loss retry it was originally written for, each
// one paid for by its own defect, and it reads better as the list it is.
//
// held is computed here and returned, because currentFrom needs the answer as
// it was BEFORE anything mutated (round forty-four).
func (l *Agent) resumeWork(op *Op) (held, changed, restoreNonce bool) {
	alias := op.SessionAlias
	// A return to a thread bound earlier is a change: nothing in the sets
	// moves, and the activation to wake does. Found by the pre-release review,
	// round eight.
	held = op.SessionID != "" && l.holdsSession(op.SessionID)
	changed = alias != "" && (!l.holdsSession(alias) || l.GuessedSession(alias))
	// A STATED session_id IS A CHANGE TOO (round thirteen), and so is any op
	// that would leave a different session current (round eight). Asked of
	// the fold's own rule rather than field by field: an identical retry
	// stating a synthetic primary beside a thread alias read as a change on
	// every call, because the primary is never the current session while
	// the thread is, and was ledgered every time. Found by the pre-release
	// review, round fifty-seven.
	changed = changed || (op.SessionID != "" && l.SessionID != op.SessionID)
	changed = changed || l.currentAfter(op, held) != l.CurrentSession
	// A GUESS CONFIRMED IS A CHANGE. A stated session_id that the row already
	// held as an inference left the guess standing, so another agent's
	// metadata could still take the active session. Found by the pre-release
	// review, round fourteen.
	changed = changed || (op.SessionID != "" && l.GuessedSession(op.SessionID))
	// A STATED PROCESS IS A CHANGE. A bridge that restarted inside the TTL
	// under the same session id stated its new pid, and this path never
	// applied one: the row kept the dead bridge's pid, the next liveness
	// sweep found it dead, and the agent that had just registered was
	// retired with its claims released. Found by the pre-release review,
	// round forty-three.
	changed = changed || (op.PID != 0 && op.PID != l.PID)
	// A NONCE TO PUT BACK IS ITSELF A CHANGE. Archival blanks Agent.Nonce and
	// keeps the nonce INDEX, so a v0.0.6 row recovered into an ACTIVE state
	// reaches this path carrying no nonce, and the restoration lived only in
	// the dormant branch of applyRegister. An agent that keeps working stays
	// live and therefore stays on this path: AgentIdentity returns "" forever
	// and the role dibs.toml grants it can never reconcile, which is the exact
	// harm Op.RestoreNonce was added to repair, reachable by the one route it
	// did not cover. Found by the pre-release review, round seventy.
	//
	// Folded into `changed` rather than done quietly, because it IS replayable
	// state: an op that changes state without advancing the serial is the
	// invariant this repository guards hardest, and the engine ledgers exactly
	// when the serial moves.
	//
	// GATED on the recorded decision, like the other site. Historical ops carry
	// no RestoreNonce, so this is false for every one of them and a v0.0.6
	// ledger replays to precisely what it did before.
	restoreNonce = op.RestoreNonce && l.Nonce == "" && op.Nonce != ""
	changed = changed || restoreNonce
	return held, changed, restoreNonce
}

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
	held, changed, restoreNonce := l.resumeWork(op)
	if op.V7Semantics && changed {
		if restoreNonce {
			l.Nonce = op.Nonce
		}
		if l.takeActivation(op) {
			// A NEW ACTIVATION RE-ARMS THE AWARENESS GATE, as the other two
			// recovery paths do: a same-nonce register inside the TTL that
			// moved the row to a new session kept the previous activation's
			// acknowledgement, and the new one could claim without a
			// check_in. Found by the pre-release review, round fifty-seven.
			l.AckedSerial = 0
		}
		s.dropTakenSession(op, l)
		if op.SessionID != "" {
			l.SessionID = op.SessionID                                         // the new session owns it now
			l.GuessedSessions = withoutString(l.GuessedSessions, op.SessionID) // stated now
		}
		l.bindHarnessSessionAs(op.SessionAlias, op.SessionGuessed)
		l.currentFrom(op, held)
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

// takeActivation records the process and location a resume states.
//
// A NEW ACTIVATION IS A NEW PROCESS. The resume moved the row to the new
// session and kept the pid and working directory of the activation that had
// ended: the next liveness sweep found that process dead and retired the
// agent that had just come back, and the board placed it where it used to
// be. What the op states is taken; a pid it does not state is unknown when
// the session moved, and kept when it did not, because the same session is
// the same process. Found by the pre-release review, round forty-three.
//
// A NEW THREAD IS A NEW ACTIVATION TOO. The bridge fills the alias from the
// harness's thread metadata and may state no session id at all, and this
// returned early on an empty session id: the thread moved, the row reported
// resumed, and the old process and location stayed. Found by the pre-release
// review, round forty-five.
// It reports whether the op moved the row to a new activation.
func (a *Agent) takeActivation(op *Op) bool {
	if op.PID != 0 {
		a.PID, a.ProcStart = op.PID, op.ProcStart
	}
	// AND A RETURN TO A THREAD BOUND EARLIER. Threads A, B, A: the third is
	// a return, the row still holds A, and "not yet held" read it as the
	// same activation as B, so B's process stayed on the row and A's stated
	// location was discarded. The activation is the CURRENT session; a
	// thread that is not it is a move, whether or not the row has seen it
	// before. Found by the pre-release review, round forty-seven.
	// A stated id the row did not have is a move. A stated THREAD that is
	// not the current session is a move too, even when the row holds it as
	// its primary: recovered through a retained thread A after the hooks
	// had moved the row to B is a return to A. The bridge's own non-thread
	// id, equal to the primary, is the activation the row is in.
	stated := op.SessionID
	movedSession := stated != "" && (stated != a.SessionID ||
		(LooksLikeThreadID(stated) && a.CurrentSession != "" && stated != a.CurrentSession))
	movedThread := LooksLikeThreadID(op.SessionAlias) && op.SessionAlias != a.CurrentSession
	if !movedSession && !movedThread {
		return false
	}
	if op.PID == 0 {
		a.PID, a.ProcStart = 0, 0
	}
	if op.Agent != nil {
		a.Agent = op.Agent
	}
	return true
}

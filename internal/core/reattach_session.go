package core

import (
	"sort"
	"time"
)

// reattachBySessionID recovers an agent that presented the same name and a
// harness session id it answers to, rotating its token.
//
// Lives beside resumeLiveAgent because the two are the same family: every way a
// returning agent gets back to its own mailbox instead of forking a sibling.
// Moved out of applyRegister when that file hit its length limit.
//
// TRUST BOUNDARY, stated precisely because it was once overstated. A session id
// is not a secret: the bridge derives one from the host's process id, which any
// same-user process can enumerate, and the agent's name is on the board. So
// presenting both rotates the token, which takes the mailbox, the actor identity
// and any role. An agent that CHOSE a nonce has a real secret and is deliberately
// not reachable this way; one that never chose is, because the alternative is
// that it can never recover at all.
//
// Returns nil when this is not that case, so the caller falls through to
// creating a new agent.
func (s *State) reattachBySessionID(op *Op, now time.Time) (Result, []Event) {
	if op.SessionID == "" || op.Nonce != "" {
		return nil, nil
	}
	l := s.pickReattachTarget(op)
	if l == nil {
		return nil, nil
	}
	l.Token = op.NewToken
	l.LastCoordination = now
	l.Status, l.StaleReason = StatusActive, ""
	l.AckedSerial = 0 // re-arm the awareness gate: this is a new activation
	if op.Agent != nil {
		l.Agent = op.Agent
	}
	if op.PID != 0 {
		l.PID, l.ProcStart = op.PID, op.ProcStart
	}
	s.dropTakenSession(op, l)
	l.bindHarnessSessionAs(op.SessionAlias, op.SessionGuessed)
	// LEDGERED, like every other transition. A branch that rotates a token and
	// returns no events never advances the serial, so the engine never writes it
	// down and replay does not reattach: after a restart the agent is stale
	// again, the new token does not work, and the OLD one comes back to life.
	evs := []Event{{Type: "agent.reattached", Agent: l.ID, Data: map[string]any{
		"via": "session_id",
	}}}
	serial := s.finish(&evs, now)
	return Result{
		"agent_id": l.ID, "token": l.Token, "serial": serial,
		"reattached": true, "via": "session_id", "board": s.Board(),
		"session_id": l.SessionID,
	}, evs
}

// pickReattachTarget chooses WHICH row a session id recovers, deterministically.
//
// ANY ID THE AGENT ANSWERS TO, not only its primary one. An agent goes by
// several: the bridge derives one, and a harness that names its own thread
// contributes another as an alias. Codex sends `threadId` in `_meta` on every
// call, so for a codex agent the identifier that identifies it is almost always
// the alias, and matching the primary alone made that the one id which would
// NOT recover it.
//
// AND DORMANT, which is where a persistent agent waits. The old test admitted
// active and stale; `stale` is where an EPHEMERAL agent lands when its lease
// lapses, and `dormant`, its persistent equivalent, was simply absent. Harmless
// while persistent agents were rare and held nonces their operators chose, and
// not harmless since persistent became the default.
//
// DETERMINISTIC, which the loop this replaced was not. It ranged over s.Agents
// and took the first match, and Go randomises map iteration. Two rows can match
// one reattach: a name that comes back is suffixed in the ID and keeps the NAME,
// so `bridgekind` and `bridgekind-3` are both named "bridgekind", and both can
// hold one thread, the first as an alias and the second as its primary. Which
// recovered was then a coin flip, and a coin flip inside the fold breaks
// state == fold(ledger): one ledger replays to different boards on different
// runs. Observed on this board within a minute of widening the match, which is
// what turned a collision from exotic into ordinary.
//
// The order is a preference, not a tie-break dressed up as one:
//
//   - a PRIMARY session id beats an alias. The primary is the id this row
//     registered under; an alias is another name it also answers to.
//   - then the liveliest status. A row still answering is likelier to be the
//     session in front of us than one that stopped days ago.
//   - then the lowest id, which decides nothing and only makes the answer
//     stable. Reaching it means two rows are indistinguishable on everything
//     that matters, and arbitrary-but-repeatable beats arbitrary.
func (s *State) pickReattachTarget(op *Op) *Agent {
	rank := map[AgentStatus]int{StatusActive: 0, StatusStale: 1, StatusDormant: 2}
	var out []*Agent
	for _, l := range s.Agents {
		// THE HISTORICAL RULE FOR HISTORICAL OPS. Matching an alias or a
		// dormant row is v0.0.7 behaviour, and this is the fold: an op written
		// under v0.0.6 that found no primary-id match created a sibling, and
		// every later op in that ledger names the sibling. Replaying it with
		// the wider match reattaches the original instead, the sibling never
		// exists, and the next op that authenticates as it fails: the daemon
		// refuses its own history. Found by the pre-release review, which is
		// the third time this repository has been caught widening Apply.
		if !op.V7Semantics {
			if l.SessionID != op.SessionID || l.Status == StatusDormant {
				continue
			}
		}
		// A CREDENTIAL THE AGENT CHOSE, not merely one it holds. Since v0.0.7
		// every registration gets a nonce whether it asked or not, so reading
		// this as "holds a nonce" refused everybody and turned every returning
		// agent into a sibling that cannot read its predecessor's mail.
		if l.Nonce != "" && !l.NonceMinted {
			continue
		}
		if _, eligible := rank[l.Status]; !eligible {
			continue
		}
		if l.Name != op.Name || !l.holdsSession(op.SessionID) {
			continue
		}
		out = append(out, l)
	}
	if len(out) == 0 {
		return nil
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if pa, pb := a.SessionID == op.SessionID, b.SessionID == op.SessionID; pa != pb {
			return pa
		}
		if rank[a.Status] != rank[b.Status] {
			return rank[a.Status] < rank[b.Status]
		}
		return a.ID < b.ID
	})
	return out[0]
}

// ReattachTarget is the row a session-id register would recover, or nil.
//
// Exported for the ingress guard, which used to carry its OWN copy of this rule
// and fell behind it: the fold began recovering rows with minted nonces and
// dormant rows, the guard still required an empty nonce and an active row, and
// an agent that had lost its context was refused with E_SESSION_TAKEN before
// the recovery it was entitled to could run. One rule, one implementation;
// found by the pre-release review. Selection only, nothing mutates.
func (s *State) ReattachTarget(op *Op) *Agent {
	if op.SessionID == "" || op.Nonce != "" {
		return nil
	}
	return s.pickReattachTarget(op)
}

// ReattachBySessionIDForTest exposes the decision to engine tests, which own the
// fixtures for who may recover whom. Exported for that and nothing else: the
// live path reaches it through applyRegister.
func (s *State) ReattachBySessionIDForTest(op *Op) (Result, []Event) {
	return s.reattachBySessionID(op, time.Now())
}

// dropTakenSession removes a session id from the row the ingress recorded as
// losing it, before this agent takes it.
//
// ONE PLACE, called before every bind. The first version of this repair was
// written inline at one of six sites that bind an alias, and check_in, the
// call every agent keeps making, binds at a different one; the test written
// for it failed on exactly that. Two stated holders of one id is a coin flip
// on every hook, so every path that can take an id has to drop it from the row
// that lost it. Read from the op and never re-decided: the ingress saw that
// the holder was not active and wrote its name down; ops written before that
// field existed on these kinds carry nothing here and replay unchanged.
//
// BOTH FIELDS. An op carries a session id in two places, the primary
// session_id a caller states and the alias the daemon joins at ingress, and
// the ingress vets both into the one SessionTakenFrom. The second version of
// this dropped only the alias, so a nonce recovery that stated a session_id
// took it and left the old holder holding it too: the coin flip this exists
// to end, on the path every reattaching agent takes. Dropping an id a row
// does not hold is nothing, so both are always dropped. Found by the
// pre-release review, round six.
func (s *State) dropTakenSession(op *Op, l *Agent) {
	if op.SessionTakenFrom == "" {
		return
	}
	prev := s.Agents[op.SessionTakenFrom]
	if prev == nil || (l != nil && prev.ID == l.ID) {
		return
	}
	prev.dropSession(op.SessionID)
	prev.dropSession(op.SessionAlias)
}

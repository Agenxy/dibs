package core

import "time"

// reattachBySessionID recovers an agent that presented the same name and
// harness session id as a live one, rotating its token.
//
// Lives beside resumeLiveAgent because the two are the same family: every way a
// returning agent gets back to its own mailbox instead of forking a sibling.
// Moved out of applyRegister when that file hit its length limit, which it had
// earned; the reasoning below came with it unchanged.
//
// Returns nil when this is not that case, so the caller falls through to
// creating a new agent.
func (s *State) reattachBySessionID(op *Op, now time.Time) (Result, []Event) {
	if op.SessionID == "" || op.Nonce != "" {
		return nil, nil
	}
	for _, l := range s.Agents {
		// A CREDENTIAL THE AGENT CHOSE, not merely one it holds.
		//
		// The rule is that a guessable id must not reattach an agent that
		// deliberately created a secret. Since v0.0.7 every registration
		// gets a nonce whether it asked for one or not, so reading this as
		// "holds a nonce" refused everybody, and the promise that
		// re-registering after context loss is always safe became false:
		// the returning agent forked a sibling that could not read its
		// predecessor's mail, which is the failure this whole branch exists
		// to prevent. Caught by the space e2e within minutes of the default
		// flipping.
		//
		// NonceMinted is false on every agent in every existing ledger, so
		// this is exactly the old behaviour for everything written before.
		if l.Nonce != "" && !l.NonceMinted {
			continue // it chose a real credential; a guessable one will not do
		}
		if l.SessionID == op.SessionID && l.Name == op.Name &&
			(l.Status == StatusActive || l.Status == StatusStale) {
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
			l.bindHarnessSessionAs(op.SessionAlias, op.SessionGuessed)
			// Ledgered for the same reason as the nonce branch above.
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
	}
	return nil, nil
}

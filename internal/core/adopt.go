package core

import "time"

// Adoption: moving an abandoned agent's mail onto a live one.
//
// Split out of apply.go because that file reached the 2000-line limit, and this
// is the one operation in the fold that touches another agent's mailbox: it is
// worth being able to read it whole.

// applyAdoptAgent moves an abandoned agent's mail onto a live one.
//
// "Abandoned" is a state, not an opinion: the source must not be active. An
// active agent is reading its own mail, and moving it would be theft dressed as
// recovery. Everything else about the source is left alone, including its
// record and its history, because the ledger refers to it and a board that
// erased the origin of six messages would be lying about where they came from.
//
// The role is NOT transferred. A role is a decision the operator made about an
// identity, and quietly carrying "coordinator" across on the strength of a
// mailbox recovery would grant a power nobody granted: `dibs admin coordinator`
// exists and is one command.
func (s *State) applyAdoptAgent(op *Op, l *Agent, now time.Time) (Result, []Event, error) {
	if !op.AdoptAuthorised {
		return nil, nil, errf("E_NOT_PERMITTED",
			"adopting another agent's mailbox is the human's call: unlock as yourself with "+
				"human_unlock, or ask them to promote you with `dibs admin coordinator <you>`",
			"adopt_agent requires the human at this machine, or a coordinator or admin")
	}
	from := s.Agents[op.To]
	if from == nil {
		return nil, nil, errf("E_NO_AGENT", "check the id on the board", "no agent %q", op.To)
	}
	into := l
	if op.Space != "" { // adopting on somebody else's behalf
		if into = s.Agents[op.Space]; into == nil {
			return nil, nil, errf("E_NO_AGENT", "check the id on the board", "no agent %q", op.Space)
		}
	}
	if from.ID == into.ID {
		return nil, nil, errf("E_BAD_TARGET", "name the abandoned agent, not the one adopting it",
			"an agent cannot adopt itself")
	}
	if from.Status == StatusActive {
		return nil, nil, errf("E_AGENT_ACTIVE",
			"an active agent is reading its own mail; there is nothing abandoned to recover",
			"agent %q is still active", from.ID)
	}
	if into.Status == StatusClosed || into.Status == StatusArchived {
		return nil, nil, errf("E_AGENT_CLOSED",
			"adopt into an agent that can still read: a retired one receives nothing",
			"agent %q is retired", into.ID)
	}
	var moved int
	for _, m := range s.Messages {
		if m.To != from.ID {
			continue
		}
		m.To = into.ID
		moved++
	}
	// The actor's durable checkpoint, which the common path sets and this one
	// returns before reaching.
	//
	// Adoption returns straight out of the dispatcher, so it misses
	// `l.LastCoordination = now` along with everything else after that point.
	// The engine's derived `seen` map hides it while the daemon runs, and that
	// map is deliberately not replayable: restart, and the adopter is judged
	// against whatever checkpoint it had BEFORE performing a ledgered
	// operation, so an active agent that has just done something can be swept
	// stale immediately. Found by a pre-release review.
	l.LastCoordination = now
	evs := []Event{{
		Type: "agent.updated", Agent: into.ID,
		Data: map[string]any{"adopted_from": from.ID, "messages": moved},
	}}
	serial := s.finish(&evs, now)
	return Result{
		"ok": true, "from": from.ID, "into": into.ID, "messages": moved,
		// WHAT MOVED IS THE MAIL THAT EXISTED, and the wording has to say so.
		//
		// This read "only where its mail is delivered has changed", which a
		// careful agent took to mean a standing redirect: it announced that it
		// was now the delivery address for that NAME and would hand the address
		// back if the original returned. Nothing here creates a rule. The loop
		// above re-addresses the messages that exist at this instant, and mail
		// sent afterwards goes to whoever it is addressed to, including the
		// original the moment it comes back.
		//
		// The difference is the whole safety of the operation: a standing
		// redirect would be a coordinator-approvable interception of a live
		// agent's mail, and this is a one-time recovery of mail nobody could
		// read. Saying it the ambiguous way invited the reader to believe the
		// dangerous one.
		"note": "read them with inbox. This moved the " + itoa(moved) + " message(s) " +
			"that existed just now, once: it is not a standing redirect. The source " +
			"agent keeps its history, and anything sent to it from here on reaches " +
			"IT, including after it comes back",
		"serial": serial,
	}, evs, nil
}

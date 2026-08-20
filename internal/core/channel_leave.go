package core

import (
	"slices"
	"time"
)

// Leaving an agent, and the repair every departure implies.
//
// A departure is never just a deletion: the leaver may hold the agent
// exclusively, may owe acknowledgements, may be the only reason a queue exists.
// Everything that depended on them has to be put right in the same step, or the
// fleet wedges behind an agent that is already gone.

func (s *State) applySpaceLeave(l *Agent, op *Op, now time.Time) (Result, []Event, error) {
	ch := s.Spaces[cleanID(op.Space)]
	if ch == nil {
		return nil, nil, errf("E_NO_SPACE", "nothing to leave", "no space %s", op.Space)
	}
	if _, ok := ch.Members[l.ID]; !ok {
		// Waiting in the queue is not membership, but it is not nothing either,
		// and this returned "not a member" and left the agent exactly where it
		// was. An exclusive space admits from the queue whenever it frees, so an
		// agent that queued, changed its mind, and was told it had left, was
		// joined to the agent anyway minutes later: handed a coordination key
		// and an obligation to acknowledge announcements in work it had
		// explicitly declined. There was no way out of a queue at all: no tool
		// removed an entry from it except promotion.
		if i := slices.Index(ch.Queue, l.ID); i >= 0 {
			ch.Queue = slices.Delete(ch.Queue, i, i+1)
			delete(ch.Pending, l.ID)
			if ch.Declined == nil {
				ch.Declined = map[string]bool{}
			}
			ch.Declined[l.ID] = true
			evs := []Event{{Type: "agent.left", Agent: l.ID, Data: map[string]any{
				"agent_id": ch.ID, "from_queue": true, "waiting": len(ch.Queue),
			}}}
			s.finish(&evs, now)
			return Result{
				"agent_id": ch.ID, "left": true, "was": "queued",
				"note": "you were waiting for this agent, not in it: you are out of the " +
					"queue and will not be admitted; join_space if you change your mind",
			}, evs, nil
		}
		return Result{"agent_id": ch.ID, "left": false, "reason": "not a member"}, nil, nil
	}
	evs := s.departChannel(ch, l.ID)
	// Deliberate, so it sticks. See Space.Declined: this is set HERE rather
	// than in departChannel because departChannel is shared with eviction and with
	// the sweep, and neither of those is a decision this agent made.
	if ch.Declined == nil {
		ch.Declined = map[string]bool{}
	}
	ch.Declined[l.ID] = true
	s.finish(&evs, now)
	return Result{
		"agent_id": ch.ID, "left": true,
		"note": "you will not be auto-joined here again: join_space if you change your mind",
	}, evs, nil
}

// departChannel removes an agent and repairs everything that depended on it.
//
// Shared with the sweep, because an agent that crashed must free its agent
// exactly as one that left politely does: a fleet wedged behind a dead owner
// is the failure mode that makes people turn coordination off.
func (s *State) departChannel(ch *Space, agent string) []Event {
	delete(ch.Members, agent)
	// Leaving an agent ends the obligations that came WITH that agent.
	//
	// Only a full close dropped them, so leave_space and evict removed the
	// membership and left the announcement still owed: `Unacked` kept
	// redelivering it, and the board kept reporting a healthy wait on somebody
	// who was no longer there. Eviction is the sharpest version: it tells the
	// agent to stop work and coordinate before resuming, while still nagging it
	// to acknowledge that agent's traffic.
	//
	// Scoped to THIS space: an agent leaving one agent still owes what it owes
	// everywhere else.
	var evs []Event
	evs = append(evs, s.dropAckRequirementsIn(ch.ID, agent)...)
	evs = append(evs, Event{Type: "agent.left", Agent: agent, Data: map[string]any{
		"agent_id": ch.ID, "members": len(ch.Members),
	}})
	dequeue(ch, agent) // an agent that left is not waiting
	if ch.Owner != agent {
		return evs
	}
	// Ownership ended. SPEC §9's honesty rule carries over verbatim: this is the
	// coordination signal ending, NOT proof the owner's processes stopped or
	// that the work is safe to take.
	ch.Owner = ""
	evs = append(evs, Event{Type: "agent.released", Agent: agent, Data: map[string]any{
		"agent_id": ch.ID,
		"caution":  "the owner's coordination signal ended; this is not proof its work stopped",
	}})
	if len(ch.Queue) == 0 {
		return evs
	}
	// Promote the first agent that can actually take it.
	//
	// This used to promote ch.Queue[0] unless it was CLOSED, but an agent that
	// crashed is `stale`, not closed, and one that is asleep is `dormant`.
	// Neither is closed, so a dead agent was handed exclusive ownership and every
	// healthy agent behind it waited on a corpse. Observed exactly: the sweep
	// marked an agent stale, the owner left, and the agent's owner became the
	// crashed agent while a live waiter sat in the queue.
	//
	// Whoever is skipped KEEPS their place, because going stale has never evicted
	// an agent from an agent and must not evict it from a queue either: a
	// persistent agent that wakes finds itself still in line.
	for i, next := range ch.Queue {
		if !s.Agents[next].CanHoldExclusive() {
			continue
		}
		ch.Queue = append(ch.Queue[:i:i], ch.Queue[i+1:]...)
		ch.promote(next, s.Serial+1)
		ch.Owner = next
		return append(evs, Event{Type: "agent.joined", Agent: next, Data: map[string]any{
			"agent_id": ch.ID, "from_queue": true, "members": len(ch.Members),
		}}, Event{Type: "agent.exclusive", Agent: next, Data: map[string]any{
			"agent_id": ch.ID, "owner": next,
		}})
	}
	// Nobody waiting can take it. Leaving the agent locked-open with a queue that
	// nothing will ever drain is the same "waiting forever" bug in another dress,
	// so this becomes a full release: the lock is gone, and everyone still
	// waiting for it becomes a member.
	return append(evs, s.releaseExclusive(ch, "no waiter could take it")...)
}

// ── exclusivity ──────────────────────────────────────────────────────────

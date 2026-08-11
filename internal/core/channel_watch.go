package core

import "time"

// Watching an agent without joining it.
//
// A subscriber sees an agent's traffic and takes no part in it: it holds no
// membership, receives no coordination key, and cannot collide with anyone. It
// is the answer to "I need to know what they decide, but this is not my work",
// which is otherwise only expressible by joining and inflating the agent.

// ReaderChannel resolves an agent for someone who may only READ it: a member, or
// a subscriber.
//
// memberChannel's own error told subscribers "subscribers read, members speak"
// while refusing every read a subscriber attempted: the hint was accurate
// about the design and wrong about the code. Subscribing exists to watch a
// agent's traffic without joining it; a subscription that cannot read is a
// subscription to nothing.
//
// The coordination key is deliberately NOT the caller's to see here. The key is
// held by membership and is the one identity claim Dibs can verify, so it goes
// to members only; LaneRead decides that, using MemberChannel.
func (s *State) ReaderChannel(l *Agent, name string) (*Space, error) {
	ch := s.Spaces[cleanID(name)]
	if ch == nil {
		return nil, errf("E_NO_LANE", "open_space or join_space first", "no agent %s", name)
	}
	if s.speaksFor(ch, l.ID) == "" && !ch.Subs[l.ID] {
		return nil, errf("E_NOT_MEMBER", "join_space to take part, or watch_space to watch",
			"not a member or subscriber of %s", ch.ID)
	}
	return ch, nil
}

// applyLaneSubscribe IS ledgered, though the argument for not ledgering it was
// tempting enough that it shipped that way: subscribing changes what one agent
// is shown, not what the fleet agreed, and nothing about it can collide.
//
// It is ledgered because Subs is not private to the subscriber. Three ledgered
// operations read it: agent.post reports len(Members)+len(Subs) as `audience`,
// merge_spaces carries subscribers across to the surviving agent, and evict
// removes them, so a fold of the ledger with Subs empty produces a different
// audience count and a different post-merge membership than the daemon that
// wrote it. state == fold(ledger) does not admit an exception for state the
// author considers unimportant; it is the reads, not the writes, that decide.
//
// The subscriber also gets the behaviour it already assumed: a subscription
// outlives a daemon restart, rather than silently lapsing while read_space keeps
// working from the pre-restart process's memory.
func (s *State) applyLaneSubscribe(l *Agent, op *Op, now time.Time) (Result, []Event, error) {
	ch := s.Spaces[cleanID(op.Space)]
	if ch == nil {
		return nil, nil, errf("E_NO_LANE", "nothing to subscribe to", "no agent %s", op.Space)
	}
	if _, isMember := ch.Members[l.ID]; isMember {
		return Result{"lane_id": ch.ID, "subscribed": false, "reason": "already a member"}, nil, nil
	}
	if op.Mode == "release" {
		if !ch.Subs[l.ID] {
			// Nothing changed, so nothing to ledger. Unsubscribing twice is not
			// an error: it is the same request arriving after a retry.
			return Result{"lane_id": ch.ID, "subscribed": false}, nil, nil
		}
		delete(ch.Subs, l.ID)
		evs := []Event{{Type: "agent.unsubscribed", Agent: l.ID, Data: map[string]any{
			"lane_id": ch.ID,
		}}}
		s.finish(&evs, now)
		return Result{"lane_id": ch.ID, "subscribed": false}, evs, nil
	}
	if ch.Subs[l.ID] {
		return Result{"lane_id": ch.ID, "subscribed": true, "topic": ch.Topic}, nil, nil
	}
	ch.Subs[l.ID] = true
	evs := []Event{{Type: "agent.subscribed", Agent: l.ID, Data: map[string]any{
		"lane_id": ch.ID, "topic": ch.Topic,
	}}}
	s.finish(&evs, now)
	return Result{"lane_id": ch.ID, "subscribed": true, "topic": ch.Topic}, evs, nil
}

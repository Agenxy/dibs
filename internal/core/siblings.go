package core

// Who a message actually reaches when the addressee is asleep, and what to tell
// the sender about it.
//
// This is its own file because the advice it gives has been wrong twice, in two
// different ways, and each cost a real fleet something.
//
// The first: a restart forked every agent, senders addressed the names they
// knew, those rows were dormant tombstones whose occupants were alive under
// `-2` ids, and Dibs accepted the mail and said it would be read "when it next
// wakes". Nobody was coming, and the failure was invisible from both ends at
// once.
//
// The second is below: a sibling shares the NAME and never the ROLE.

// liveSiblingOf finds an active agent sharing this one's name.
//
// Deliberately NOT siblingByName, which ranks by how much mail an agent holds
// because it answers a different question. "which mailbox can the caller not
// read". Here the only thing that matters is which sibling is ALIVE, and
// borrowing that ranking would quietly skip a live agent that happened to hold
// less mail than a dead one.
func (s *State) liveSiblingOf(to *Agent) *Agent {
	for _, l := range s.Agents {
		if l.ID != to.ID && l.Name == to.Name && l.Status == StatusActive {
			return l
		}
	}
	return nil
}

// sleepingNote explains a delivery to an agent that will not read it soon.
func (s *State) sleepingNote(to *Agent) string {
	if !to.Sleeping() {
		return ""
	}
	var note string
	// A sleeping agent that has been SUPERSEDED will never wake, so the
	// reassurance below is false for it and has to be said differently.
	//
	// This is the case that lost two full bug reports on a live fleet. A
	// restart forked every agent; agents addressed mail to the names they knew;
	// those agents were dormant tombstones whose occupants were now alive under
	// `-2` ids. Dibs accepted the mail and told the senders it would be seen
	// "when it next wakes". Nobody was coming. The failure was invisible from
	// both ends at once: the sender read success, the intended recipient saw
	// nothing, and it took a third space to notice.
	if live := s.liveSiblingOf(to); live != nil {
		note = "delivered to " + to.ID + ", which is " + string(to.Status) +
			", but " + live.ID + " is LIVE under the same name and is almost certainly who " +
			"you meant. " + to.ID + " will not wake to read this. Resend to " + live.ID + "."
		// A sibling shares the NAME. It does not inherit the role, and
		// saying "almost certainly who you meant" is wrong when the reason
		// you meant the first one was its authority.
		//
		// Measured within an hour of the reclaim path shipping: an agent
		// needed a coordinator to approve an adoption, addressed the one
		// holding the role, found it dormant, followed this advice to its
		// live sibling, and asked an agent with no role at all. It opened by
		// telling that agent it held the coordinator role, because this note
		// had said so in everything but the word. Neither end could see the
		// authority had been dropped in transit.
		if to.IsCoordinator() && !live.IsCoordinator() {
			note = note + " NOTE: " + to.ID + " holds the " +
				"coordinator role and " + live.ID + " does NOT: a sibling shares the " +
				"name, never the role. If you addressed " + to.ID + " for its " +
				"AUTHORITY (approving an adoption or a grant, force_release, evict), " +
				"resending to " + live.ID + " reaches somebody who cannot act. Ask the " +
				"human instead: the board row marked `human: true`."
		}
	} else {
		// The message is already committed by the time we get here, so the note
		// must say what IS true, not warn about what might happen: it is queued
		// and will be delivered; only the deadline is at risk.
		note = "delivered to " + to.ID + ", which is currently " + string(to.Status) +
			": it will see this when it next wakes. The message is not lost; only the response " +
			"deadline is at risk, so re-send with a larger deadline_s if you need an answer, or " +
			"use notify/handoff when you do not."
	}
	return note
}

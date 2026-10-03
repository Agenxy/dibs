package core

// Addressing: turning what a caller WROTE into the row it names.
//
// An agent has two strings on the board and for nine releases only one of them
// was an address. The `id` is the row's key, minted from the name the agent first
// asked for and suffixed when that was taken; the `name` is the label it may
// revise with `update`. Every ledger record that names an agent, every
// message's to/from, every claim owner, every space membership, every role
// pin, names it by id, and that is why the id is immutable: a mutable address
// is a message delivered to the wrong agent.
//
// THE DEFECT THAT PRODUCED THIS FILE, observed on a live board on 2026-09-26.
// An agent registered as `codex-primary`, renamed itself `gpt-dibs`, and its
// row then read id: codex-primary, name: gpt-dibs. A peer that did exactly what
// the board invites and addressed `gpt-dibs` was told no such agent exists,
// while the string that did work was the one the board no longer showed as the
// agent's identity. A rename is what an agent does when it CHANGES ROLE, so
// every peer that learned the new role learned an address reaching nobody, and
// the address that worked was one no reader of the board would pick.
//
// The fix is NOT to move the mailbox. It is to make the name an address too, so
// discovery and addressing agree without anything that already worked changing
// its meaning. The id keeps working forever, because it never stops being the
// row's key: there is no alias to expire and no mail to lose.
//
// WHERE THIS IS CALLED FROM, and why it matters that it is not called from the
// fold: Engine.exec resolves a written reference into the op before Apply, and
// the op that reaches the ledger therefore carries an ID. That is the same
// mechanism `to: "coordinator"` has always used, for the same reason a role
// address needed it: a name MOVES, so an op that recorded one could be replayed
// into a delivery to whoever holds that name at replay time. Resolving at
// ingress leaves Apply completely untouched, which is what keeps
// `state == fold(ledger)` true of every ledger written before this existed.

// agentRefScope says which rows a reference may resolve to.
type agentRefScope bool

const (
	// anyRow is addressing. An exact id wins whatever state its row is in,
	// because the calls that name a retired agent are the ones that clean up
	// after it: prune's whole job is rows in that state, and an archived agent
	// can still be sent mail (see Agent.Retired).
	anyRow agentRefScope = false
	// liveRow is a reference that must reach somebody who can act: the role
	// pin in dibs.toml, where a retired row must not shadow the live agent
	// that took its name over. See Engine.resolveConfiguredAgentDecision,
	// which is where that was learned and which now asks this.
	liveRow agentRefScope = true
)

// AgentRef resolves an address a caller wrote into the id that reaches it.
//
// Returns ("", candidates) when the reference is a name more than one eligible
// row holds, and ("", nil) when it names nothing at all. A caller that gets
// nothing passes the literal through unchanged, so the refusal the fold would
// have given, with the hint that names the nearest live agents, is still the one
// the caller reads. Nothing here ever invents an address.
func (s *State) AgentRef(ref string) (id string, ambiguous []string) {
	return s.agentRef(ref, anyRow)
}

// LiveAgentRef is AgentRef for a reference that has to reach an agent able to
// act: a retired row neither wins as an exact id nor answers as a name.
func (s *State) LiveAgentRef(ref string) (id string, ambiguous []string) {
	return s.agentRef(ref, liveRow)
}

func (s *State) agentRef(ref string, scope agentRefScope) (string, []string) {
	if ref == "" {
		return "", nil
	}
	// AN EXACT ID WINS OUTRIGHT, and this ordering is the compatibility
	// guarantee rather than a preference: every op that resolved before this
	// existed resolved by id, so every one of them resolves to the same row
	// now. A namesake can never overtake an address.
	//
	// The comparison is exact. An id is a lowercase slug and a name is whatever
	// the agent chose, so case-folding here would make two distinct labels one
	// address and put a guess on the delivery path.
	if l, ok := s.Agents[ref]; ok && (scope == anyRow || !l.Gone()) {
		return ref, nil
	}
	// Then the label. Live rows first, so a retired row never shadows the agent
	// that took its name over: that is the failure the role pin hit, where a
	// retired `fleet-lead` resolved forever and the live `fleet-lead-2` holding
	// the name was never considered.
	var live, retired []string
	for _, id := range sortedKeys(s.Agents) {
		l := s.Agents[id]
		if l == nil || l.Name != ref {
			continue
		}
		if l.Gone() {
			retired = append(retired, id)
		} else {
			live = append(live, id)
		}
	}
	candidates := live
	if len(candidates) == 0 && scope == anyRow {
		candidates = retired
	}
	switch len(candidates) {
	case 0:
		return "", nil
	case 1:
		return candidates[0], nil
	}
	// AMBIGUITY IS REFUSED, NEVER GUESSED. The single failure this board exists
	// to prevent is work, or a message, landing on the wrong agent, and there is
	// no ranking that makes picking one of two live mailboxes honest. The caller
	// is handed both ids and addresses one.
	return "", candidates
}

// ErrAmbiguousAgent is the refusal for a name more than one live agent holds.
//
// Built here rather than at the ingress site so the hint is the same wherever a
// reference is resolved, and so it says the thing a drifted caller needs: the
// ids, which are unambiguous by construction.
func ErrAmbiguousAgent(ref string, candidates []string) error {
	return errf("E_AMBIGUOUS_AGENT",
		"address one of them by id: "+joinAnd(candidates)+". An id is unique; a name is a "+
			"label agents choose and two of them chose this one",
		"%q is the name of %d agents", ref, len(candidates))
}

// joinAnd renders a candidate list the way a sentence needs it.
func joinAnd(xs []string) string {
	switch len(xs) {
	case 0:
		return ""
	case 1:
		return xs[0]
	}
	out := ""
	for i, x := range xs[:len(xs)-1] {
		if i > 0 {
			out += ", "
		}
		out += x
	}
	return out + " or " + xs[len(xs)-1]
}

// shadowedNameNote is the clause register's name_note adds when the name an
// agent asked for is still another row's id.
//
// A name addresses an agent, but an id wins when a reference is resolved
// (AgentRef), so this row is shown under a label that resolves to the older
// agent. update() is refused from taking a peer's id for exactly this reason;
// register cannot refuse, because suffixing is what lets a new agent start at
// all. So it is said instead, on the one call that can still be answered by
// picking another name. Empty when the id is free.
func (s *State) shadowedNameNote(want, name string) string {
	if _, ok := s.Agents[want]; !ok {
		return ""
	}
	return ". A name is an address now and yours is " + name + ", but an id " +
		"wins over one: a peer writing to " + want + " resolves to that agent " +
		"rather than to you. Call update(name=…) with something free if you want " +
		"your label to address you"
}

// nameIsAnotherAddress refuses a rename onto a string that already addresses
// a different row.
//
// The sibling check in applyUpdate asks who else is CALLED this; this asks who
// else is REACHED by it. The two came apart the moment a name became an
// address. An id wins when a reference is resolved, so taking a peer's id as
// your label publishes a name that delivers to the peer: the board's id/name
// defect with the arrow reversed. siblingByName cannot see it, because a row
// whose id and name still agree is caught by the name and a row that has
// renamed itself once is not.
//
// ANY ROW'S ID, retired ones included, because AgentRef's first rule is blind
// to status: an id resolves while the row exists at all. A rename onto a closed
// agent's id would publish a label that resolves to a mailbox nothing can be
// delivered to, which is worse than the live case rather than better. It is
// also the reason register refuses to reuse an id: the ledger's history refers
// to it.
//
// Checked by State.Admit, never Apply: older ledgers may hold this rename,
// and replay must continue accepting it without a new wire flag.
func (s *State) nameIsAnotherAddress(op *Op, l *Agent) error {
	other, taken := s.Agents[op.Name]
	if !taken || other.ID == l.ID {
		return nil
	}
	return errf("E_NAME_TAKEN",
		"pick another name: "+op.Name+" is an ADDRESS, and an id wins over a "+
			"name when a peer resolves one, so that label would resolve to "+
			other.ID+" rather than to you",
		"%q is the id of %s, which is on the board (%s)",
		op.Name, other.ID, other.Status)
}

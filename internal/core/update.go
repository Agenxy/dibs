package core

// The update fold and its session helpers, split from apply.go at its size limit.
// Admission of new name rules stays separate from this historical fold.

func (s *State) applyUpdate(l *Agent, op *Op) (Result, []Event, error) {
	// The size bounds for this op are in Admit, not here. A bound in the fold is
	// retroactive, and this one was found by the test that asserts Apply folds
	// whatever Admit rejects, once that test learned about update.
	// A RELEASE OF NOTHING IS NOT AN EVENT. Rule 2: an op is ledgered iff it
	// changed replayable state, and the engine ledgers exactly when the serial
	// advanced, so the two must never disagree.
	//
	// release_session cleared the primary, the aliases and the provenance and
	// then said session_released: true whatever it found, so calling it against
	// an agent with nothing bound advanced the serial and appended an op that
	// changed nothing, and told the caller a binding had been taken away. Its
	// test only ever exercised a populated binding. Found by the pre-release
	// review.
	//
	// Checked FIRST because everything below mutates, and gated on V7Semantics
	// because a fold that stops advancing where it used to is retroactive: an
	// older ledger holding one of these expects the serial to move.
	if op.V7Semantics && op.ReleaseSession && bareRelease(op, l) && !l.hasSessionBinding() {
		return Result{
			"ok": true, "id": l.ID, "name": l.Name, "description": l.Description,
			"session_released": false,
			"session": "nothing to release: no session id, alias or guess was bound " +
				"to you, so nothing changed and nothing was recorded",
		}, nil, nil
	}
	res := Result{"ok": true, "id": l.ID}
	// Taking a live agent's name is refused, not suffixed. Register suffixes
	// because a new agent has no history to protect; here both agents already
	// exist, and two live agents sharing a name is not cosmetic: liveSiblingOf
	// redirects a dead agent's mail to a same-named live one, so a rename onto
	// somebody else's name is a mail-redirection primitive.
	if op.Name != "" && op.Name != l.Name {
		if other := s.siblingByName(op.Name, l.ID); other != nil {
			return nil, nil, errf("E_NAME_TAKEN",
				"pick another name, or leave name out and update only your description",
				"the name %q belongs to %s, which is still on the board: two live agents "+
					"sharing a name redirects mail between them", op.Name, other.ID)
		}
		res["renamed_from"] = l.Name
		res["address"] = "your id is still " + l.ID + ", it never changes. Peers may now " +
			"address you as " + op.Name + "; former names remain aliases unless released " +
			"or shadowed by an id or another agent's current name. The send result names the actual recipient"
		l.Name = op.Name
	}
	l.Description = op.Description
	if op.Agent != nil {
		res["identity"] = l.mergeIdentity(op.Agent)
	}
	s.dropTakenSession(op, l)
	if sid := l.bindHarnessSessionAs(op.SessionAlias, op.SessionGuessed, op.V7Semantics); sid != "" {
		res["session_id"] = sid
	}
	// A participant that HAS no process says so, which is the only way to clear
	// a pid recorded earlier: omitting one means "unchanged", so the register
	// path cannot express this at all. It is also the only path that reliably
	// can, because register short-circuits a same-nonce retry inside one TTL and
	// returns the original result without applying anything: correct for a
	// retried registration, and silently a no-op for a correction spelled as
	// one. Asked for by the human's row, which recorded the DAEMON's pid and so
	// reported the operator as a dead process after every restart.
	if op.NoProcess {
		l.PID, l.ProcStart = 0, 0
		res["process"] = "no process recorded: liveness is silence from here on"
	}
	// The repair for a binding that is already wrong. See Op.ReleaseSession:
	// only ever the caller's own, so it can strand nothing but itself.
	if op.ReleaseSession {
		had := l.SessionID
		aliases := len(l.SessionAliases) + len(l.GuessedSessions)
		bound := l.hasSessionBinding()
		l.SessionID, l.SessionAliases, l.GuessedSessions, l.CurrentSession = "", nil, nil, ""
		// Honest even when this op DID change something else, which is the case
		// the early return above deliberately does not cover.
		res["session_released"] = bound
		// ALIASES COUNTED, not just the primary. This named only `had`, so an
		// agent holding a working alias and no primary read "released no
		// primary session id" while the alias it was actually reached by had
		// just been taken away. Found by the pre-release review.
		res["session"] = "released " + quoteOrNone(had) + " and " + itoa(aliases) +
			" alias(es): lifecycle hooks quoting any of them now reach nobody until " +
			"an agent binds them again, and the sessions they belong to can claim " +
			"them back. Re-register or check_in from that session to bind your own"
	}
	res["name"], res["description"] = l.Name, l.Description
	data := map[string]any{"name": l.Name, "created_serial": l.CreatedSerial}
	if previous, ok := res["renamed_from"].(string); ok {
		data["renamed_from"] = previous
	}
	if len(op.ReleaseNames) > 0 {
		data["release_names"] = append([]string(nil), op.ReleaseNames...)
		res["released_names"] = append([]string(nil), op.ReleaseNames...)
	}
	return res, []Event{{Type: "agent.updated", Agent: l.ID, Data: data}}, nil
}

// hasSessionBinding reports whether anything would actually be taken away by a
// release: the primary, any alias, or the provenance of a guessed one.
func (a *Agent) hasSessionBinding() bool {
	return a.SessionID != "" || len(a.SessionAliases) > 0 || len(a.GuessedSessions) > 0
}

// bareRelease reports whether this update asks for nothing but the release.
//
// Description is compared rather than assumed absent: an empty description
// CLEARS, because ledgers already hold update ops whose recorded effect was
// exactly that, so "unset" cannot mean "leave alone" here.
func bareRelease(op *Op, l *Agent) bool {
	return op.Name == "" && op.Description == l.Description &&
		op.Agent == nil && op.SessionAlias == "" && !op.NoProcess && len(op.ReleaseNames) == 0
}

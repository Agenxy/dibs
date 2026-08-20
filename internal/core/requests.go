package core

// Requests that PERFORM what they ask for.
//
// An ordinary request is prose: somebody asks, somebody approves, and the
// approval is a recorded agreement that a thing should happen. That left the
// last move to a human at a terminal, and it is where the loop kept dying.
// Measured twice on a live board: a coordinator grant approved and never
// applied, and a returning agent told to reclaim its mailbox through a call it
// had no authority to make.
//
// So a request can carry a TYPED field naming the effect, and approving it is
// the effect. `Grant` is a role; `Adopt` is an abandoned mailbox. Both are
// ledgered on the message, because a request replayed without the field would
// be an approval of nothing.
//
// The rules that keep this from being self-service are split by what each layer
// knows. This file owns the vocabulary: which values are legal, and on which
// message type. The ENGINE owns identity: that a grant may only be requested
// from the human, and that approving an adoption needs the authority performing
// one needs. core does not know that humans exist, and that ignorance is what
// keeps it a pure state machine.

// checkGrantRequest rejects a role request that must not be answerable by a
// press.
//
// Called from Admit, NOT from Apply, and that is the whole point of where it
// sits. This is payload vocabulary: which roles are legal and on which message
// type. Apply is also the fold that replays a ledger written by older code, so
// a vocabulary rule enforced there is retroactive, and the day the accepted set
// changes is the day the daemon refuses to boot on its own history. It was
// written into Apply first and caught by review before release, which is the
// fourth time this exact mistake has been made in this repository.
//
// ADMIN is refused, permanently and on purpose. Coordinator is breadth:
// broadcast and force_release, and it deliberately cannot read anybody's mail.
// Admin is the god view, every agent's decrypted mail included, and the entire
// reason the board sits behind Touch ID or a password is that reading everyone's
// mail is not a thing to hand over on a notification tapped between two others.
// It stays on the human's own path.
//
// Refused on anything but a request, too. A notify carrying a grant would be a
// role change with no decision attached, and a question is answered with prose,
// so there would be no yes for the grant to hang on.
//
// Who may RECEIVE one is not decided here: core does not know that humans exist,
// which is the rule that keeps this package a pure state machine. The engine
// holds that check, next to the identity it already owns.
func checkGrantRequest(op *Op) error {
	// One approval performs one effect.
	//
	// `grant` and `adopt` were admitted independently and BOTH executed on
	// approval, while the human's prompt is a grant-first switch that renders
	// only the grant. So a request could read "make me coordinator?" on the
	// operator's screen and, on the single yes that answers it, also move a
	// dormant agent's entire mailbox onto the asker. The operator approved one
	// thing and performed two, and nothing in the prompt or the response said
	// so.
	//
	// Refused rather than fixed in the display. A prompt that lists every effect
	// is still a prompt somebody skims, and the honest shape is that a yes means
	// exactly what it was asked for. Two effects are two requests, which the
	// operator can approve separately or refuse separately.
	//
	// Found by a pre-release review; the display bug and the missing exclusion
	// were each individually survivable and together made a hidden capability.
	if op.Grant != "" && op.Adopt != "" {
		return errf("E_BAD_ARG",
			"send them as two requests: approving one is a single yes, and it has to "+
				"mean one thing. The operator can then approve the role and refuse the "+
				"mailbox, or the other way round",
			"a request carries a grant or an adoption, never both")
	}
	if op.Adopt != "" && op.MsgType != MsgRequest {
		return errf("E_BAD_TYPE",
			`send it as type "request": approving one is what moves the mailbox`,
			"a %s cannot carry an adoption", op.MsgType)
	}
	if op.Grant == "" {
		return nil
	}
	if op.MsgType != MsgRequest {
		return errf("E_BAD_TYPE",
			`send it as type "request": approving one is what performs the grant, `+
				"and a request is the only type with an approve",
			"a %s cannot carry a grant", op.MsgType)
	}
	switch op.Grant {
	case RoleCoordinator, RoleMember:
		return nil
	case RoleAdmin:
		return errf("E_BAD_ROLE",
			"admin reads every agent's mail, so it is never granted by approving a "+
				"notification: ask your human to run `dibs admin admin <you>` themselves",
			"admin cannot be requested")
	default:
		return errf("E_BAD_ROLE",
			"grant takes coordinator (broadcast + force_release) or member (hand it back)",
			"unknown role %q", op.Grant)
	}
}

// decideRequestEffects works out what approving this message performs, without
// performing it: the caller applies the result once the message itself has been
// moved to its terminal state.
//
// Split out because applyRespond is the fold's busiest function and this is a
// separate decision with four of its own refusals, not a step in recording an
// answer.
func (s *State) decideRequestEffects(m *Message, op *Op, st string) (granted, adopted *Agent, err error) {
	// Approving a role request IS the grant.
	//
	// It used to record an agreement that one should happen, after which the
	// person went to a terminal and typed `dibs admin coordinator <agent>`. Two
	// steps for one decision, and the second is where it died: the approval sat
	// answered on the board while the agent stayed unable to do the thing it had
	// just been told it could.
	//
	// The authority is already checked, six lines up: a request can only be
	// answered by the agent it was addressed to, and the engine refuses to send
	// one carrying a grant to anybody but the human. Nothing here lets an agent
	// promote itself. It lets a person's yes mean yes.
	if st == MsgStateApproved && m.Grant != "" {
		if granted = s.Agents[m.From]; granted == nil {
			return nil, nil, errf("E_NO_AGENT",
				"nothing was granted; the requester is gone",
				"no agent %q to grant %q to", m.From, m.Grant)
		}
	}
	// An approved adoption MOVES the mailbox, here, rather than telling the
	// asker it now has permission to go and do it.
	//
	// The board is full of evidence for why: dibs-maintainer, -2 and -3;
	// codex-root and -2; codex-1 and -2. Every one of those is an agent that
	// came back, could not prove it was itself, and started again beside its own
	// unread mail. The recovery path existed and needed an authority the asker
	// did not have, so the reachable answer was always "register again".
	//
	// Authorised exactly as adopt_agent is, and by the same field: the engine
	// decides whether this responder may adopt and records it, because a rule
	// that read roles at replay time would re-decide history against a board
	// whose roles have since changed.
	if st == MsgStateApproved && m.Adopt != "" {
		if !op.AdoptAuthorised {
			return nil, nil, errf("E_NOT_PERMITTED",
				"moving somebody's mailbox is the human's call or a coordinator's: this "+
					"request has to be approved by one of them",
				"approving an adoption requires the human at this machine, or a coordinator")
		}
		into := s.Agents[m.From]
		if into == nil {
			return nil, nil, errf("E_NO_AGENT", "nothing was moved; the requester is gone",
				"no agent %q to adopt into", m.From)
		}
		from := s.Agents[m.Adopt]
		if from == nil {
			return nil, nil, errf("E_NO_AGENT", "check the id on the board",
				"no agent %q to adopt", m.Adopt)
		}
		if from.ID == into.ID {
			return nil, nil, errf("E_BAD_TARGET",
				"name the abandoned agent, not the one reclaiming it",
				"an agent cannot adopt itself")
		}
		if from.Status == StatusActive {
			return nil, nil, errf("E_AGENT_ACTIVE",
				"an active agent is reading its own mail; there is nothing abandoned here",
				"agent %q is still active", from.ID)
		}
		adopted = from
	}
	return granted, adopted, nil
}

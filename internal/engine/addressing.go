package engine

import "github.com/agenxy/dibs/internal/core"

// Resolving the agent a caller NAMED into the id the ledger records.
//
// See internal/core/address.go for what the board's id/name split cost and why
// the name is an address now. This file is the other half: the ingress step that
// turns a written reference into an id BEFORE the op is applied, so the fold
// and every ledger already on disk are untouched.
//
// It sits beside the role address (`to: "coordinator"`) in Engine.exec for the
// same reason, stated there: a name moves, so an op recording one could be
// replayed into a delivery to whoever holds that name at replay time.
//
// WHERE IN exec, PRECISELY: before everything downstream that compares op.To
// against an id, which is all of it. The human's deadline, the grant gate, the
// human-mailbox guard, the empty-adoption check and the ambiguous-release
// refusal each read op.To expecting an address.
//
// The reads are a separate matter and do NOT pass through here: Engine.AllMail
// resolves its own `agent`, on the loop, and says why.
//
// DEFAULT ON, WITH THE EXCEPTIONS NAMED. The alternative shape, a list of the op
// kinds that DO address an agent, is the one this repository has paid for
// repeatedly: a rule applied at one call site and not its siblings, where the
// next op to reuse `to` silently misses it and nobody finds out until an agent
// addresses somebody and reaches nobody. Here the failure mode is inverted. A
// new op that names an agent in `to` gets the rule for free, and a field that
// names something ELSE has to say so below, which is a compile-time-visible
// omission rather than a silent one.
//
// Resolution is also, deliberately, incapable of changing an outcome that
// already worked: an exact id resolves to itself (core.AgentRef's first rule),
// and a reference that matches nothing is passed through so the fold's own
// refusal, with the hint naming the nearest live agents, is what the caller
// reads. It can only ever turn a refusal into a delivery.

// spaceValuedTo names the ops whose `to` is a SPACE id, not an agent.
//
// merge_spaces is the only one today: `to` is the space being merged INTO. An
// agent whose name happened to match that space id would otherwise capture the
// field and send the merge at something that is not a space.
var spaceValuedTo = map[string]bool{
	core.OpSpaceMerge: true,
}

// agentValuedSpace names the ops whose `space` is an AGENT, not a space.
//
// adopt_agent alone, where `space` carries the heir the mailbox moves onto. The
// field is reused rather than named for what it holds, which is exactly why it
// needs listing: nothing about the name says an agent id lives in it.
var agentValuedSpace = map[string]bool{
	core.OpAdoptAgent: true,
}

// agentRef is one field on an op that names an agent, and the word the SCHEMA
// uses for it: the note built from these is read by whoever wrote the argument,
// so it has to name the parameter they typed rather than the Go field.
type agentRef struct {
	label string
	at    *string
}

// resolveAgentRefs rewrites every reference to an agent on this op into an id,
// and reports the ones that were not already one.
//
// Refuses, rather than guessing, when a name belongs to more than one live
// agent: see core.AgentRef. Nothing is ledgered on a refusal, because this runs
// before admission, Apply and ledger append. Admission checks canonical IDs.
func (e *Engine) resolveAgentRefs(op *core.Op) (resolved map[string]string, err error) {
	if e.state == nil {
		return nil, nil
	}
	// `to: "coordinator"` reaches whoever holds the role.
	//
	// An agent asking for its identity back does not know, and should not have
	// to look up, which of sixteen rows is the coordinator today. The role is
	// the address; the id is an implementation detail that changes when somebody
	// hands the role over. Resolved at ingress, so the LEDGER records the agent
	// it actually went to: a message addressed to a role, replayed after the
	// role moved, would otherwise be delivered to somebody it was never sent to.
	//
	// FIRST, and that is what makes the role win over a name: "coordinator" is
	// a role rather than a label, and an agent is free to call itself one.
	if op.Kind == core.OpSendMessage && op.To == core.RoleCoordinator {
		who := e.state.CoordinatorID()
		if who == "" {
			return nil, core.ErrNoCoordinator
		}
		op.To = who
	}
	refs := make([]agentRef, 0, 3)
	// AND "human" IS THE OTHER ROLE, resolved later in exec by
	// prepareHumanRecipient, which mints the person's mailbox on demand. It is
	// left untouched here for the reason "coordinator" is resolved first: a
	// role wins over a label. Nothing stops a local agent naming itself
	// `human` (the person's row is named after the OS user, and only an
	// invitation reserves the word), so resolving it as a name sent the
	// person's mail to that agent instead.
	humanRole := op.Kind == core.OpSendMessage && op.To == core.HumanActor
	if !spaceValuedTo[op.Kind] && !humanRole {
		refs = append(refs, agentRef{"to", &op.To})
	}
	// merge_agents names two rows and both are addresses. The one that was
	// missed here would be the one that keeps the seat.
	refs = append(refs, agentRef{"merge_into", &op.MergeInto})
	if agentValuedSpace[op.Kind] {
		refs = append(refs, agentRef{"into", &op.Space})
	}
	for _, ref := range refs {
		was := *ref.at
		id, ambiguous := e.state.AgentRef(was)
		if len(ambiguous) > 0 {
			return nil, core.ErrAmbiguousAgent(was, ambiguous)
		}
		if id == "" || id == was {
			continue
		}
		*ref.at = id
		if resolved == nil {
			resolved = map[string]string{}
		}
		resolved[ref.label] = was
	}
	return resolved, nil
}

// addressedNote tells a caller which id its written reference reached.
//
// Said out loud for the reason every honesty rule here exists: the caller wrote
// a label and the mail went to an id, and the id is the thing to quote
// afterwards, to a coordinator or in a handoff. A name is a label its owner may
// change again tomorrow.
func addressedNote(op *core.Op, resolved map[string]string) string {
	note := ""
	for _, label := range []string{"to", "merge_into", "into"} {
		was, ok := resolved[label]
		if !ok {
			continue
		}
		var id string
		switch label {
		case "to":
			id = op.To
		case "merge_into":
			id = op.MergeInto
		case "into":
			id = op.Space
		}
		if note != "" {
			note += "; "
		}
		note += label + ": " + was + " is the NAME of " + id
	}
	if note == "" {
		return ""
	}
	return note + ". Resolved to the id, which is the address that never moves: quote " +
		"that when you refer to this agent, because a name is a label its owner may revise"
}

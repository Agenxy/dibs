package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// GrantRole sets an agent's role. It is reachable only from the daemon's admin
// path (local secret + admin password), which is why it takes no agent token:
// promotion is a human decision, never an agent's.
func (e *Engine) GrantRole(ctx context.Context, agent, role string) (core.Result, error) {
	return e.Do(ctx, &core.Op{Kind: core.OpGrantRole, To: agent, Mode: role})
}

// GrantRoleByHuman is GrantRole for a decision a person made: it stands
// against the startup reconciler for the rest of this run.
func (e *Engine) GrantRoleByHuman(ctx context.Context, agent, role string) (core.Result, error) {
	return e.Do(ctx, &core.Op{Kind: core.OpGrantRole, To: agent, Mode: role, RoleByHuman: true})
}

// Broadcast sends one message to every other live agent. Coordinator-only.
//
// It is deliberately N ordinary sends rather than one clever op: each message
// keeps its own serial, its own ledger entry, its own mailbox accounting and
// deadline. Replay stays exact and a broadcast is indistinguishable from the
// coordinator having written to each agent by hand, which is what it is.
func (e *Engine) Broadcast(ctx context.Context, token, msgType, body string) (core.Result, error) {
	// Resolve the sender and the recipient set inside the loop, so the fan-out
	// is taken from one consistent snapshot of the board.
	snap, err := e.query(ctx, func() core.Result {
		l, errRes := e.authRead(token, time.Now())
		if errRes != nil {
			return errRes
		}
		if !l.IsCoordinator() {
			return core.Result{"error": core.ErrNotCoordinator}
		}
		ids := []string{}
		for _, to := range e.state.LiveAgentsExcept(l.ID) {
			ids = append(ids, to.ID)
		}
		return core.Result{"ids": ids}
	})
	if err != nil {
		return nil, err
	}
	if e2, ok := snap["error"].(error); ok {
		return nil, e2
	}
	ids, _ := snap["ids"].([]string)

	delivered := []uint64{}
	failed := map[string]string{}
	for _, id := range ids {
		res, err := e.Do(ctx, &core.Op{
			Kind: core.OpSendMessage, Token: token, To: id, MsgType: msgType, Body: body,
		})
		if err != nil {
			// Honest partial delivery: say who did not get it and why, rather
			// than reporting a success that did not happen.
			failed[id] = err.Error()
			continue
		}
		if ser, ok := res["msg_serial"].(uint64); ok {
			delivered = append(delivered, ser)
		}
	}
	out := core.Result{"ok": true, "delivered": len(delivered), "msg_serials": delivered}
	if len(failed) > 0 {
		out["undelivered"] = failed
		out["warning"] = "some agents did not receive this: see undelivered"
	}
	return out, nil
}

// AllMail returns every message, decrypted: the god view, for an agent the human
// promoted to admin. It is the one place an agent may read mail it is not party
// to, which is exactly why it is gated on the admin role and nothing weaker.
//
// With census set it returns COUNTS per mailbox and never a body, and that a
// coordinator may have. Custody and contents are different capabilities and
// only one of them is sensitive: consolidating stranded registrations is a
// coordinator's job, and to place a mailbox it needs one fact, whether there
// is anything in it and whether anyone is still waiting on an answer. The
// only door to that fact was this call, which refused, so a coordinator
// approved a consolidation while telling the requester it could not check.
// Two of the three mailboxes turned out to hold nothing. Issue #77.
func (e *Engine) AllMail(ctx context.Context, token string, census bool, agent string) (core.Result, error) {
	res, err := e.query(ctx, func() core.Result {
		l, errRes := e.authRead(token, time.Now())
		if errRes != nil {
			return errRes
		}
		if census {
			if !l.IsCoordinator() {
				return core.Result{"error": core.ErrNotCoordinator}
			}
			return core.Result{"census": e.mailboxCensus(agent), "serial": e.state.Serial}
		}
		if !l.IsAdmin() {
			return core.Result{"error": core.ErrNotAdmin}
		}
		// `agent` selects one mailbox here as it does for the census. The
		// schema said so and this path never read it, so an admin asking for
		// one worker's mail was handed every mailbox on the board. Round
		// five of the pre-release review.
		out := make([]*core.Message, 0, len(e.state.Messages))
		for _, m := range e.state.Messages {
			if agent != "" && m.To != agent {
				continue
			}
			out = append(out, m)
		}
		return core.Result{"messages": out, "serial": e.state.Serial}
	})
	if err != nil {
		return nil, err
	}
	if e2, ok := res["error"].(error); ok {
		return nil, e2
	}
	return res, nil
}

// MailboxCensus is what a coordinator may know about a mailbox it cannot read:
// how much is in it, of what, from whom, how old, and how much of it is still
// waiting on an answer. No bodies, no responses, no attachments. It is the same
// distinction the board already makes: who is blocked on whom, never what they
// said.
type MailboxCensus struct {
	Agent  string           `json:"agent"`
	Status core.AgentStatus `json:"status"`
	// Messages counts everything addressed to the agent that the board still
	// holds, which is exactly what adopt_agent would move.
	Messages int            `json:"messages"`
	ByType   map[string]int `json:"by_type,omitempty"`
	// AwaitingAnswer counts questions and requests nobody has answered: the
	// ones that need a live holder today, as opposed to a row that wants a
	// prune.
	AwaitingAnswer int `json:"awaiting_answer"`
	// Unread counts mail the agent never retrieved.
	Unread  int       `json:"unread"`
	Senders []string  `json:"senders,omitempty"`
	Oldest  time.Time `json:"oldest,omitzero"`
	Newest  time.Time `json:"newest,omitzero"`
}

// mailboxCensus counts every non-retired agent's mailbox, or one agent's when
// named (a named agent with nothing in it is reported with zeros rather than
// omitted: "empty" is the answer the caller came for). Called on the loop.
func (e *Engine) mailboxCensus(only string) []MailboxCensus {
	rows := map[string]*MailboxCensus{}
	for id, l := range e.state.Agents {
		if only != "" && id != only {
			continue
		}
		if only == "" && l.Retired() {
			continue
		}
		rows[id] = &MailboxCensus{Agent: id, Status: l.Status}
	}
	// THROUGH Inbox, WHICH IS THE ONE DEFINITION OF WHAT IS IN A MAILBOX.
	// This walked every retained message addressed to the id, which is a
	// THIRD answer to that question: the watermark that fences a
	// predecessor's mail did not apply, and neither did readable(), so a
	// notify somebody had already acknowledged was counted. A coordinator
	// deciding "recover or prune" was told `messages: 1` about a mailbox
	// whose adoption then answered E_NOTHING_TO_ADOPT. core.readable's
	// own comment says adoption had a second definition and what that
	// cost; this is the same mistake one call further out. Round
	// fifty-three of the pre-release review.
	senders := map[string]map[string]bool{}
	for id, row := range rows {
		for _, m := range e.state.Inbox(id) {
			row.count(m)
			if senders[id] == nil {
				senders[id] = map[string]bool{}
			}
			senders[id][m.From] = true
		}
	}
	out := make([]MailboxCensus, 0, len(rows))
	for id, row := range rows {
		for from := range senders[id] {
			row.Senders = append(row.Senders, from)
		}
		sort.Strings(row.Senders)
		out = append(out, *row)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Agent < out[j].Agent })
	return out
}

// count folds one message into the row: how many, of what, still waiting,
// never retrieved, and the age range. Nothing from the body.
func (r *MailboxCensus) count(m *core.Message) {
	r.Messages++
	if r.ByType == nil {
		r.ByType = map[string]int{}
	}
	r.ByType[m.Type]++
	if m.Expecting() && !m.Terminal() {
		r.AwaitingAnswer++
	}
	if m.State == core.MsgStatePending {
		r.Unread++
	}
	if r.Oldest.IsZero() || m.SentAt.Before(r.Oldest) {
		r.Oldest = m.SentAt
	}
	if m.SentAt.After(r.Newest) {
		r.Newest = m.SentAt
	}
}

// RolePinFingerprint is the durable identity of an agent holding a nonce.
//
// Exported because the daemon has to compute the EXPECTED fingerprint from the
// operator's config, and a second copy of this hash in cmd/dibd would be a
// second answer to "who is this agent" waiting to disagree with the first.
// The nonce itself never leaves the engine; only this does.
func RolePinFingerprint(nonce string) string {
	if nonce == "" {
		return ""
	}
	sum := sha256.Sum256([]byte("dibs-role-pin\x00" + nonce))
	return hex.EncodeToString(sum[:])
}

// ResolveConfiguredAgent turns what an operator wrote in `[roles]` into an
// agent id.
//
// THE CONFIG TAKES A NAME; THE STATE IS KEYED BY ID. `docs/CONFIGURATION.md`
// documents these values as agent names, register slugs a name into an id, and
// the reconciler passed the string straight through to a map lookup: the
// documented `admin = ["Fleet Lead"]` waited forever for an agent whose id was
// literally `Fleet Lead`, while the agent that registered under that name was
// sitting there as `fleet-lead`. Validation accepted it and the daemon logged,
// every fifteen seconds, that the agent had not registered. Anything with a
// capital, a space, an underscore or a slash in it was affected, which is most
// of how a person writes a name.
//
// AMBIGUITY IS REFUSED, not guessed. Two agents may share a display name, since
// the id collision is resolved with a suffix, and picking one of them would
// hand a standing role to whichever the map happened to yield. This is an
// authorisation path; a name that names two agents names none.
//
// It grants nothing on its own. The fingerprint in `[roles.identity]` still has
// to match, so resolving a name only decides WHOSE fingerprint is checked.
func (e *Engine) ResolveConfiguredAgent(ctx context.Context, nameOrID string) (string, error) {
	res, err := e.query(ctx, func() core.Result {
		return e.resolveConfiguredAgentDecision(nameOrID)
	})
	if err != nil {
		return "", err
	}
	if amb, _ := res["ambiguous"].([]string); len(amb) > 0 {
		return "", fmt.Errorf("%q names %d agents (%s): a standing role cannot be "+
			"granted to a name that does not identify one. Use the agent id, which "+
			"the board shows", nameOrID, len(amb), strings.Join(amb, ", "))
	}
	id, _ := res["id"].(string)
	if id == "" {
		return "", &core.Error{
			Code: "E_NO_AGENT",
			Msg:  nameOrID + " has not registered",
			Hint: "nothing to do: the reconciler retries every tick, and this is the " +
				"ordinary state of a board whose agents have not started yet",
		}
	}
	return id, nil
}

// resolveConfiguredAgentDecision is the decision, split from the loop plumbing:
// query() sends on e.ops, which is nil on an engine whose loop is not running,
// so a test calling the wrapper would block forever rather than fail.
//
// Callers run on the writer loop.
func (e *Engine) resolveConfiguredAgentDecision(nameOrID string) core.Result {
	if e.state == nil || nameOrID == "" {
		return core.Result{}
	}
	// An exact id wins outright. An operator who wrote the id meant the id, and
	// it cannot be ambiguous.
	//
	// A GONE ONE DOES NOT. The by-name branch below already refuses closed and
	// archived agents and this did not, and a name IS the first agent's id: a
	// retired `fleet-lead` therefore shadowed the live `fleet-lead-2` that had
	// taken the name over, so the documented handover resolved the predecessor
	// forever and the successor was never considered. It fails closed, at the
	// pin, which is the right direction and still leaves the board without the
	// coordinator its config names.
	if l, ok := e.state.Agents[nameOrID]; ok && !l.Gone() {
		return core.Result{"id": nameOrID}
	}
	var byName []string
	for id, l := range e.state.Agents {
		if l != nil && l.Name == nameOrID && !l.Gone() {
			byName = append(byName, id)
		}
	}
	sort.Strings(byName)
	switch len(byName) {
	case 0:
		return core.Result{}
	case 1:
		return core.Result{"id": byName[0]}
	}
	return core.Result{"ambiguous": byName}
}

// AgentByIdentity finds the agent whose credential has this fingerprint, on
// any row the board still holds: archived rows included, since a role on an
// archived row is a role, and a name is not consulted, since a name is a
// label the agent can change. It answers the withdrawal question, "who did
// this mechanism grant to", from the one fact the pin recorded. "" when no
// row carries that credential any more.
func (e *Engine) AgentByIdentity(ctx context.Context, fingerprint string) (string, error) {
	if fingerprint == "" {
		return "", nil
	}
	res, err := e.query(ctx, func() core.Result {
		seen := map[string]bool{}
		ids := make([]string, 0, 1)
		for id, l := range e.state.Agents {
			if l != nil && l.Nonce != "" && RolePinFingerprint(l.Nonce) == fingerprint {
				seen[id] = true
				ids = append(ids, id)
			}
		}
		// AND THE NONCE INDEX. A sweep written before v0.0.8 blanked the
		// row's nonce on archival and kept the index entry that lets the
		// agent resume, so on a replayed board the credential lives in
		// state.Nonces alone: a search of the rows found nobody, the pin was
		// dropped, and the archived holder could resume into the role the
		// config had withdrawn. Round six of the pre-release review.
		for nonce, id := range e.state.Nonces {
			if seen[id] || e.state.Agents[id] == nil || RolePinFingerprint(nonce) != fingerprint {
				continue
			}
			seen[id] = true
			ids = append(ids, id)
		}
		sort.Strings(ids)
		if len(ids) == 0 {
			return core.Result{}
		}
		return core.Result{"id": ids[0]}
	})
	if err != nil {
		return "", err
	}
	id, _ := res["id"].(string)
	return id, nil
}

// AgentIdentity is an opaque, stable fingerprint of the agent's own credential,
// or "" when it has none.
//
// A FINGERPRINT, never the nonce. The caller that needs this is the standing
// role reconciler, which has to answer "is the agent holding this name still the
// one I granted the role to", and answering it must not require handing the
// agent's only durable secret to anything outside the engine.
//
// An agent with no nonce has no durable identity at all: it cannot prove it is
// itself after a restart, and a standing role is precisely a thing that outlives
// restarts. The empty string says so, and the reconciler refuses rather than
// pinning nothing.
func (e *Engine) AgentIdentity(ctx context.Context, id string) (string, error) {
	res, err := e.query(ctx, func() core.Result {
		l, ok := e.state.Agents[id]
		if !ok {
			return core.Result{"missing": true}
		}
		if l.Nonce == "" {
			return core.Result{"fingerprint": ""}
		}
		return core.Result{"fingerprint": RolePinFingerprint(l.Nonce)}
	})
	if err != nil {
		return "", err
	}
	if missing, _ := res["missing"].(bool); missing {
		return "", fmt.Errorf("no agent %q", id)
	}
	fp, _ := res["fingerprint"].(string)
	return fp, nil
}

// AgentRole reports the role an agent holds, without changing anything.
//
// It exists because the only way to ask used to be GrantRole, and reading the
// answer that way GRANTS it: a probe for "is this agent admin?" made it admin
// and then reported that it was not, because the grant had changed something.
// An inspector that mutates the thing it inspects is worse than no inspector.
func (e *Engine) AgentRole(ctx context.Context, id string) (string, error) {
	res, err := e.query(ctx, func() core.Result {
		l, ok := e.state.Agents[id]
		if !ok {
			return core.Result{"missing": true}
		}
		return core.Result{"role": l.Role}
	})
	if err != nil {
		return "", err
	}
	if missing, _ := res["missing"].(bool); missing {
		return "", fmt.Errorf("no agent %q", id)
	}
	role, _ := res["role"].(string)
	return role, nil
}

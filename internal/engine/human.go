// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"log/slog"
	"os"
	"os/user"
	"strings"
	"sync"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// The human as a participant.
//
// Until now the board was a window: the human could watch the fleet and could
// not speak to it. Every affordance: join an agent, post, answer a question,
// existed for agents only, and the web board's own test asserted "the operator
// view offers no actions". That is a coherent design for a monitoring tool and
// the wrong one for a coordination service, because the human is the one
// participant who always has context the agents lack.
//
// THE DESIGN CHOICE, and it is the whole of this file: the human is not a new
// kind of actor with a parallel permission surface. **The human gets an AGENT
// identity**, a participant, exactly like any other, and every action goes
// through the same tools with the same token. Nothing in internal/core learns
// that humans exist.
//
// They get no LANE of their own. An agent is a space of work; the human joins
// the ones agents open, reads them, and speaks in them. A space with one
// person in it coordinates nobody.
//
// That is worth stating because the alternative is tempting and much worse: a
// set of admin-only write endpoints that mutate state directly would be a second
// authorization path into the state machine, unledgered by construction unless
// each one remembered to ledger, and invisible to `dibs verify`. Routing the
// human through an ordinary agent means their messages are ordinary messages,
// their memberships replay like anyone's, and an agent cannot tell, or need to
// tell: whether it is talking to a person.
//
// AUTHENTICATION is inherited, not invented. The web board already sits behind
// a session cookie that can only be minted by proving the admin password
// (cmd/dibd/guard.go). Reaching this code means that already happened, so the
// token below is handed out on that basis and never leaves the daemon.
type humanState struct {
	mu    sync.Mutex
	token string
	agent string
}

// HumanIdentity reports the human's agent id WITHOUT creating one.
//
// Reading the board must not make you a participant. An operator who opens the
// page to see what the fleet is doing has joined nothing, declared nothing and
// owes nobody an acknowledgement: registering them on page load would put a
// permanent agent on the roster, count them in the fleet, and subject them to
// liveness sweeps, all for looking.
//
// Empty means "not registered yet", which the board renders as observing.
// HumanIdentity reads the board through the loop, so it is safe to call from an
// HTTP handler or a reporting goroutine. In-loop callers want
// humanIdentityLocked, which must not re-enter.
func (e *Engine) HumanIdentity() string {
	if e.ops == nil {
		// A zero-value Engine has no loop; query would block forever rather than
		// fail, which is the trap AGENTS.md warns about.
		return e.humanIdentityLocked()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := e.query(ctx, func() core.Result {
		return core.Result{"id": e.humanIdentityLocked()}
	})
	if err != nil {
		return ""
	}
	id, _ := res["id"].(string)
	return id
}

// humanIdentityLocked is the same answer for a caller already inside the loop.
//
// The split exists because HumanIdentity is called from BOTH sides: an HTTP
// handler (off the loop, racing the writer) and the send path inside Do (on the
// loop, where re-entering would deadlock). One function could not be correct for
// both, and it was the off-loop callers that were quietly wrong.
func (e *Engine) humanIdentityLocked() string {
	e.human.mu.Lock()
	defer e.human.mu.Unlock()
	if e.human.token != "" {
		if e.state.AgentByToken(e.human.token) != nil {
			return e.human.agent
		}
		e.human.token, e.human.agent = "", ""
	}
	// The token is this daemon RUN's; the identity is the board's.
	//
	// Holding only the token meant the human stopped being the human at every
	// restart, until they unlocked again. Everything keyed off this then went
	// quiet: the board stopped marking their row, and mail addressed to them
	// stopped raising a notification, which is the one path that exists because
	// the person is not in a loop to notice its absence.
	//
	// The registration itself is durable: it is in the ledger, minted with a
	// known nonce. Recovering the id from that is reading what is already
	// there, not registering anybody, so the rule above still holds: opening
	// the board makes nobody a participant.
	if id, ok := e.state.Nonces[humanNonce()]; ok {
		if l := e.state.Agents[id]; l != nil && l.Status != core.StatusArchived {
			return id
		}
	}
	return ""
}

// humanRowLocked is the human's agent id whatever state that row is in.
//
// Separate from humanIdentityLocked, which answers "who may act as the human"
// and rightly says nobody once the row is archived. Ownership is not the same
// question as authority: an archived human still owns the mail in their
// mailbox, and for the week the row survives retention, asking the authority
// question about ownership let a coordinator adopt it.
func (e *Engine) humanRowLocked() string {
	return e.rowForNonce(humanNonce())
}

// dibsRowLocked is the daemon's OWN reporting row, whatever state it is in.
//
// Reserved on the same terms as the human's and for the same reason stated at
// wouldTakeHumanIdentity: `dibs` is what says "Dibs found a fault", on a board
// where an agent reading that has no way to check who wrote it. That guard
// covers the row while a nonce is presented; this one covers it when none is,
// which is the case a v0.0.6 archive-and-recovery creates by blanking the
// nonce field while keeping the index. Found by the pre-release review, round
// seventy-five.
func (e *Engine) dibsRowLocked() string {
	return e.rowForNonce(dibsNonce())
}

// rowForNonce resolves a minted identity through the nonce INDEX, which
// survives the archival that blanks Agent.Nonce.
func (e *Engine) rowForNonce(nonce string) string {
	if e.state == nil {
		return ""
	}
	if id, ok := e.state.Nonces[nonce]; ok {
		if l := e.state.Agents[id]; l != nil {
			return id
		}
	}
	return ""
}

// humanNonce is the recovery credential the human's agent is registered with.
// One place, because HumanAgent writes it and HumanIdentity reads it, and a
// second spelling would make the human two different people.
func humanNonce() string { return "human:" + humanName() }

// HumanAgent returns the human's AGENT id and token, creating it on first use.
//
// Called only from an ACTION: joining an agent, posting, sending a message. The
// identity comes into being the moment the operator first does something, and
// not before.
//
// An agent, not an agent. In SPEC-CHANNELS.md's vocabulary an agent is a
// participant, an identity, a mailbox, a token, and an agent is a space of
// work. The human needs the former and emphatically not the latter: they
// monitor and join the spaces agents create, and a space of their own would
// be a room with one person in it.
//
// (The Go type is still `core.Agent` because the ledger's wire name for a
// participant is frozen as `agent`. That collision is documented at the top of
// internal/core/space.go, and it is easy to fall into: this function was
// called HumanAgent until a reader pointed out it reads as "the human gets a
// space", which is the opposite of what it does.)
//
// PERSISTENT, and registered with a stable nonce, so the same person reattaches
// to the same identity across daemon restarts rather than accumulating a
// graveyard of `ada-2`, `ada-3`. Their mail and their agent memberships are the
// things that must survive, and both hang off that identity.
func (e *Engine) HumanAgent(ctx context.Context) (agent, token string, err error) {
	res, err := e.query(ctx, func() core.Result {
		id, tok, mintErr := e.humanAgentLocked(time.Now())
		if mintErr != nil {
			return core.Result{"error": mintErr}
		}
		return core.Result{"agent_id": id, "token": tok}
	})
	if err != nil {
		return "", "", err
	}
	id, _ := res["agent_id"].(string)
	tok, _ := res["token"].(string)
	return id, tok, nil
}

// The single mint path, called on the writer loop by the web action and an
// authenticated send to the human role. Never re-enter Do/query from here,
// and never hold human.mu across exec: both would deadlock the single writer.
// Only this board's OS identity supplies the name, nonce and process policy.
func (e *Engine) humanAgentLocked(now time.Time) (agent, token string, err error) {
	e.human.mu.Lock()
	cachedID, cachedTok := e.human.agent, e.human.token
	e.human.mu.Unlock()
	if cachedTok != "" && e.state.AgentByToken(cachedTok) != nil {
		return cachedID, cachedTok, nil
	}

	name := humanName()
	// The OS name is the initial label, not the recovery key. A renamed row
	// still belongs to this person through the reserved nonce. Re-registering
	// its old label after dormancy/restart is E_NONCE_IN_USE; recover the row's
	// current label first, including when archival cleared its nonce field.
	if id := e.humanRowLocked(); id != "" {
		// rowForNonce checks the row exists; this is on the single writer loop.
		name = e.state.Agents[id].Name
	}
	res, err := e.exec(&core.Op{
		Kind: core.OpRegister, Name: name,
		// The one registration allowed to be this identity. See core.Op.HumanMint.
		HumanMint:   true,
		Description: "the human at the board",
		AgentKind:   core.KindPersistent,
		Nonce:       humanNonce(),
		SessionID:   humanNonce(),
		// No pid, deliberately. A person is not a process, and this used to be
		// the DAEMON's pid: after a restart the sweep probed a dead process and
		// reported the operator as `process_exited`. Their liveness is silence,
		// governed by idle_ttl like any agent that registers without one.
		NoProcess: true,
		Agent: &core.AgentInfo{
			Harness: "dibs web", Surface: "web", Host: hostname(),
		},
	}, now)
	if err != nil {
		return "", "", err
	}
	tok, _ := res["token"].(string)
	id, _ := res["agent_id"].(string)
	if tok == "" || id == "" {
		return "", "", core.ErrBadToken
	}
	// The awareness gate applies to the human exactly as it does to an agent
	// (SPEC §6): you may not declare work before acknowledging what others are
	// doing. Doing it here rather than making the UI do it keeps the rule in one
	// place. On mailbox creation this precedes the incoming message; it does
	// not assert that the person has read or answered that message.
	if _, err := e.exec(&core.Op{Kind: core.OpAckBoard, Token: tok}, now); err != nil {
		return "", "", err
	}
	e.human.mu.Lock()
	e.human.token, e.human.agent = tok, id
	e.human.mu.Unlock()
	return id, tok, nil
}

// Resolve only after the sender passed structural validation, authentication
// and rate admission. Reads still create nothing. The ledger stores the actual
// recipient, never a role that might resolve differently when replayed.
func (e *Engine) prepareHumanRecipient(op *core.Op, now time.Time) error {
	if op.Kind != core.OpSendMessage {
		return nil
	}
	if op.To == "human" {
		id, _, err := e.humanAgentLocked(now)
		if err != nil {
			return err
		}
		op.To = id
	}
	human := e.humanIdentityLocked()
	// Only agent-directed asks use delivery-start semantics. A person's
	// notification route is independent of inbox reads and keeps its existing
	// fixed response deadline. Record this decision in the new send op so old
	// ledger entries replay with their original send-time deadline.
	op.DeliveryStart = op.To != human &&
		(op.MsgType == core.MsgQuestion || op.MsgType == core.MsgRequest)
	// A person's reply may take hours. Preserve the human default, recorded
	// into the op for replay, for both role and concrete-id addresses.
	if human != "" && op.To == human && op.DeadlineSec == 0 && op.MsgType != core.MsgNotify {
		op.DeadlineSec = int(humanDeadline / time.Second)
	}
	// Only the person may grant a role; addressing the alias cannot bypass
	// that existing authorization rule.
	if op.Grant != "" && (human == "" || op.To != human) {
		return core.ErrGrantNeedsHuman
	}
	return nil
}

// RepairHumanProcess clears a pid recorded against the human by older code.
//
// The fix above stops the daemon writing its own pid into a person's row, but
// it only fixes rows written after it, and the wrong pid is already in the
// ledger of every board that ran the old build. Nothing heals it on its own:
// the human's registration is written on an ACTION, so a person who reads their
// board and closes it is told `process gone` about themselves forever, which is
// both false and the exact opposite of the honesty the board is for.
//
// Gated on the row EXISTING and holding a pid, so it repairs and never
// registers: a board whose operator has never acted still has no human on it,
// which is the rule HumanIdentity's comment sets out. Gated on the pid so it is
// a one-time correction rather than an op every daemon start, which would grow
// the ledger by a record a day to say nothing.
func (e *Engine) RepairHumanProcess(ctx context.Context) {
	// The decision is read THROUGH the loop; only the answer comes back out.
	//
	// This read Nonces, Agents and a token directly, from a startup goroutine,
	// while the writer was already serving. e.human.mu protects the cached human
	// fields and nothing in core's maps, so that was a plain data race and a
	// candidate for a concurrent map read-and-write panic. Found by an
	// independent review before release.
	ctx2, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	res, err := e.query(ctx2, func() core.Result {
		id, ok := e.state.Nonces[humanNonce()]
		if !ok {
			return core.Result{}
		}
		l := e.state.Agents[id]
		if l == nil || l.Status == core.StatusArchived || l.PID == 0 {
			return core.Result{}
		}
		return core.Result{"id": id, "token": l.Token}
	})
	if err != nil {
		return
	}
	id, _ := res["id"].(string)
	token, _ := res["token"].(string)
	if id == "" || token == "" {
		return
	}
	l := struct{ Token string }{token}
	// An update, not a registration. Spelling a correction as a register looks
	// natural and does nothing: register short-circuits a same-nonce retry
	// inside one TTL and returns the original result without applying the op, so
	// the repair silently succeeded and changed nothing on exactly the boards
	// where the row was fresh. Update revises a participant's recorded facts,
	// which is what this is.
	//
	// The description is restated because update takes what it is given: it is
	// a full statement of what the participant says about itself, and omitting
	// it clears it.
	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpUpdate, Token: l.Token,
		Description: "the human at the board",
		NoProcess:   true,
	}); err != nil {
		// Not fatal and not worth failing a boot over: the board is wrong in one
		// row until the operator next acts, which is where it was before.
		slog.Warn("could not clear the stale pid on the human's row", "agent", id, "err", err)
	}
}

// HumanTouch refreshes the human's liveness without writing anything.
//
// A person reading the board is present in every sense that matters, but they
// produce no ops while reading, so without this the sweep would eventually mark
// them stale and release any agent they own out from under them mid-read.
func (e *Engine) HumanTouch(ctx context.Context) {
	e.human.mu.Lock()
	tok := e.human.token
	e.human.mu.Unlock()
	if tok == "" {
		return
	}
	_, _ = e.query(ctx, func() core.Result {
		if l := e.state.AgentByToken(tok); l != nil {
			e.seen[l.ID] = time.Now()
		}
		return core.Result{}
	})
}

// humanName supplies the initial label and reserved nonce's OS identity.
// The display label may change; the nonce remains tied to the OS username.
// The OS username is the default because an agent reading
// "ada asked you to stop" learns something, and "human-1" does not.
func humanName() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return sanitiseName(u.Username)
	}
	if n := os.Getenv("USER"); n != "" {
		return sanitiseName(n)
	}
	return "human"
}

func sanitiseName(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			out = append(out, r)
		case r >= 'A' && r <= 'Z':
			out = append(out, r+32)
		default:
			out = append(out, '-')
		}
	}
	if len(out) == 0 {
		return "human"
	}
	return string(out)
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return ""
	}
	return h
}

// mayAdopt reports whether this caller may take over an abandoned mailbox.
//
// Three ways, and no fourth. The human proven present at this machine, because
// the mail is theirs and Touch ID is the one identity here that cannot be
// asserted. An admin, who can already read every mailbox, so this grants
// nothing new. A coordinator, whose whole job is clearing debris nobody else
// can.
//
// Not "an agent in the same directory", which is the shape that keeps looking
// reasonable and is exactly how AgentForHook once handed a stranger somebody
// else's private mail.
func (e *Engine) mayAdopt(l *core.Agent) bool {
	if l.Role == "admin" || l.Role == "coordinator" {
		return true
	}
	// Token authentication already proved the caller. The reserved human
	// identity survives replay; a per-run credential cache does not. Use the
	// active identity here, not mailbox ownership of an archived human row.
	return l.ID != "" && l.ID == e.humanIdentityLocked()
}

// refuseCoordinatorSelfAdoption keeps custody and contents apart on the one
// move where they collapse into each other.
//
// A coordinator may say where a mailbox goes; it may only read one that was
// addressed to it. Moving an abandoned mailbox onto a THIRD party gives the
// coordinator nothing, and is the consolidation the role exists for. Moving
// it onto ITSELF makes the coordinator the reader of everything that was
// sent to that name (the move is once, not a standing redirect: adoptNote):
// that is the coordinator granting itself read access to another agent's
// mail, which is the human's call, the way human_unlock is. An admin reads
// every mailbox already, and the human unlocked as itself IS the human; only
// the plain coordinator is refused here. The approval path is untouched: a coordinator approving another
// agent's request to adopt makes that agent the reader, not itself. Issue #77.
func (e *Engine) refuseCoordinatorSelfAdoption(actor *core.Agent, op *core.Op) error {
	if !op.AdoptAuthorised || actor.IsAdmin() || e.isTheHuman(actor.ID) {
		return nil
	}
	if op.Space != "" && op.Space != actor.ID && actor.OtherHands(e.state.Agents[op.Space]) {
		return nil // onto somebody else: custody only
	}
	// "Somebody else" is decided by OtherHands, not by comparing ids. The
	// two-call route round three of the pre-release review found: register
	// a second agent from the same session, keep its token, adopt `into` it,
	// read with that token. A row minted from a session the coordinator holds
	// is a name the coordinator holds the token for; and a row minted with no
	// session at all (round seven: a stateless caller that sent none) cannot
	// be told from one, so it is not a third party either.
	if op.Space != "" && op.Space != actor.ID {
		return &core.Error{
			Code: "E_NOT_PERMITTED",
			Msg: "a coordinator may move a mailbox onto a third party, and " + op.Space +
				" cannot be shown to be one",
			Hint: "that agent was registered from your own session, or with no session the " +
				"board could record, so it is not distinguishable from you under another " +
				"name. Have it register through its own harness's bridge (or bind_session " +
				"with its session id) and try again, or unlock as yourself with human_unlock",
		}
	}
	return &core.Error{
		Code: "E_NOT_PERMITTED",
		Msg:  "a coordinator may move a mailbox but may not move one onto itself",
		Hint: "adopting " + op.To + " onto yourself makes you the reader of everything sent " +
			"to that name, which is the human's call: unlock as yourself with human_unlock, " +
			"or adopt it onto the agent that should hold it with `into`. To decide where " +
			"it belongs without reading it, all_mail(census: true, agent: " + op.To + ") counts " +
			"what is in it and who is still waiting on an answer",
	}
}

// refuseApprovingOwnAdoption closes the other door to the same room.
//
// refuseCoordinatorSelfAdoption stops `adopt_agent` from making a coordinator
// the reader of an abandoned mailbox. The request route reached the same
// effect: a coordinator sends ITSELF a request carrying `adopt`, approves it,
// and decideRequestEffects moves the mailbox onto the requester, which is the
// coordinator, with no human involved. Approving a request the responder sent
// is not a decision anybody else made, so it carries none of the authority
// that approval exists to record. Same exemptions as the direct rule: an
// admin reads everything already, and the human unlocked as itself is the
// human. Found by the pre-release review.
func (e *Engine) refuseApprovingOwnAdoption(actor *core.Agent, op *core.Op) error {
	if op.Disposition != "approve" || actor.IsAdmin() || e.isTheHuman(actor.ID) {
		return nil
	}
	m, ok := e.state.Messages[op.MsgSerial]
	if !ok || m == nil || m.Adopt == "" {
		return nil
	}
	// The requester is the coordinator itself, or the coordinator under
	// another name (SameHands): an agent it registered from its own session
	// asking for a mailbox it will then read with the token register handed
	// the coordinator. Round three of the pre-release review.
	if m.From != actor.ID && actor.OtherHands(e.state.Agents[m.From]) {
		return nil
	}
	return &core.Error{
		Code: "E_NOT_PERMITTED",
		Msg:  "a coordinator may not approve its own request to adopt a mailbox",
		Hint: "approving a request you sent makes you the reader of everything sent to " +
			m.Adopt + ", which is the human's call: unlock as yourself with human_unlock, or " +
			"adopt it onto the agent that should hold it with adopt_agent(into: ...)",
	}
}

// respondAsHuman records the person's answer as an ordinary response from their
// own agent. One place, because every path that answers on their behalf has to
// mint the same identity and go through the same op.
func (e *Engine) respondAsHuman(serial uint64, disposition, body string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, token, err := e.HumanAgent(ctx)
	if err != nil {
		slog.Warn("could not answer for the human; the message stays open", "msg", serial, "err", err)
		return
	}
	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpRespond, Token: token, MsgSerial: serial,
		Disposition: disposition, Body: body,
	}); err != nil {
		slog.Warn("the human answered but the response did not land", "msg", serial, "err", err)
	}
}

// report says when a notification could not be delivered. Silence here is the
// failure mode this whole path exists to remove, so it must not fail silently
// itself.
// A method now, so the failure reaches the coordinator and not only the log.
// A log line is a report to nobody: the operator is not tailing it, which is
// the same reason this whole notification path exists.
func (e *Engine) report(err error) {
	if err != nil {
		slog.Warn("could not notify the human; the message is still on the board", "err", err)
		e.reportNotifyFailure(err)
	}
}

// mayApproveGrant refuses a role grant approved by anybody but the person.
//
// The send-time check is not enough, and the gap is not obvious. The engine
// verifies that a request carrying `grant` is ADDRESSED to the human when it is
// sent. Adoption then rewrites the `to` of every message in a mailbox, and a
// coordinator is allowed to adopt. So:
//
//  1. an agent asks the human for coordinator;
//  2. the human's row goes dormant, which needs no help: they are a person, and
//     silence is their whole liveness model;
//  3. an existing coordinator adopts that mailbox, which it is permitted to do;
//  4. the pending request is now addressed to the coordinator;
//  5. it approves, and promotes the asker with no human anywhere in the story.
//
// Found by an independent review before release. The rule this restores is the
// one the feature was built on: a role is a human's to give, and "addressed to
// the human" has to be true at the moment somebody says yes, not merely at the
// moment somebody asked.
//
// Refused at ingress, so nothing is ledgered and replay never sees it.
func (e *Engine) mayApproveGrant(actor *core.Agent, op *core.Op) error {
	if op.Disposition != "approve" {
		return nil // denying a grant needs no authority; it grants nothing
	}
	m := e.state.Messages[op.MsgSerial]
	if m == nil {
		return nil
	}
	human := e.humanIdentityLocked()
	if m.Grant != "" && (human == "" || actor.ID != human) {
		return core.ErrGrantNeedsHuman
	}
	// The mailbox half asks humanRowLocked, for the reason written on
	// guardHumanMailbox: an archived human still owns their mail. Approving a
	// request that carries `adopt: <the human>` is the same taking, one door
	// along, and this one was skipped entirely once the row aged out.
	// The same rule as adopt_agent, at the OTHER door.
	//
	// guardHumanMailbox covered the direct call and nothing else, so an agent
	// could send a request carrying `adopt: <the human>`, a coordinator could
	// approve it in good faith, and the operator's whole mailbox moved to the
	// asker. Found by an independent reviewer constructing exactly that.
	//
	// The lesson is the shape rather than the instance: an effect with two entry
	// points needs its rule at the effect, and this is the second escalation in
	// one release from guarding a door instead. Both doors now consult this
	// sentence, and there are only two.
	if row := e.humanRowLocked(); m.Adopt != "" && row != "" && m.Adopt == row && actor.ID != row {
		return core.ErrHumanMailboxIsTheirs
	}
	return nil
}

// guardHumanMailbox stops anybody but the person adopting the person's mail.
//
// core/roles.go states the coordinator boundary outright: "It gets no power to
// *read* another agent's mail. Breadth, not intrusion." Adoption moves a
// mailbox, and reading it afterwards is the point, so a coordinator adopting
// the HUMAN's identity walks straight through that sentence and collects every
// private message anybody ever sent the operator, plus any pending request
// addressed to them.
//
// A person's row is dormant most of the time by design, since silence is their
// entire liveness model, so "not active" is not a signal that their mail is
// abandoned. It is the normal state of a human who is not currently typing.
//
// The human may still adopt their own identity, which is what recovery looks
// like for them.
func (e *Engine) guardHumanMailbox(actor *core.Agent, target string) error {
	// THE ROW, not the ACTING identity.
	//
	// humanIdentityLocked answers "who may act as the human", and it correctly
	// returns nothing once that row is archived: an archived identity cannot
	// approve anything. This guard asks a different question, and reading the
	// same answer made it fail open at exactly the wrong moment. Thirty days
	// dormant archives the human, the row and its mail survive the seven-day
	// retention window, and core accepts an archived source as abandoned, so a
	// coordinator could adopt the operator's private mailbox into its own inbox
	// during that week: directly, or by approving a peer's request carrying it.
	//
	// Whose mail it is does not change when they stop typing.
	human := e.humanRowLocked()
	if human == "" || target != human || actor.ID == human {
		return nil
	}
	return core.ErrHumanMailboxIsTheirs
}

// whoIs describes the agent behind a request, for a person deciding whether to
// say yes.
//
// "Dibs · make asker coordinator?" is not enough to approve a privilege change
// on, and the operator said so on seeing one: "I don't know who the asker is,
// that's a gap and security risk." A name is whatever the agent called itself,
// chosen in its first seconds and changeable with `update`; on a board where
// three rows are variations of one word it identifies nobody.
//
// So this composes the line from what makes the agent FINDABLE: the id the
// daemon assigned and the board addresses, then where it is working and on what
// machine. A person can match that against a window they have open.
//
// It is still, mostly, the agent's own word. The harness and cwd arrive from
// the bridge but nothing stops an agent asserting them, and this must not imply
// otherwise: the id is the only part the daemon issues, which is exactly why the
// id leads and is never replaced by the display name. Read it as "who this
// claims to be", and treat a request you cannot place as one to deny.
func whoIs(l *core.Agent) string {
	parts := []string{l.ID}
	if invitedAgent(l) {
		parts = append(parts, "invited cloud agent")
	}
	if l.Name != "" && l.Name != l.ID {
		parts = append(parts, "(displayed as "+l.Name+")")
	}
	if a := l.Agent; a != nil {
		where := a.Project
		if where == "" {
			where = a.CWD
		}
		switch {
		case a.Harness != "" && where != "":
			parts = append(parts, a.Harness+" in "+where)
		case a.Harness != "":
			parts = append(parts, a.Harness)
		case where != "":
			parts = append(parts, where)
		}
		if a.Branch != "" {
			parts = append(parts, "on "+a.Branch)
		}
		if a.Host != "" {
			parts = append(parts, "at "+a.Host)
		}
	}
	return strings.Join(parts, " · ")
}

// humanDeadline is how long a question or request to a PERSON waits before it
// expires.
//
// A day, because that is the honest unit for somebody who answers when they
// next look at their machine rather than on their next turn. Well inside the
// seven days core allows a persistent recipient, so a sender who wants longer
// can still ask for it, and a sender who wants shorter can still say so.
//
// Not forever: a request nobody answers has to end, or the board fills with
// asks whose context is long gone and whose askers have moved on.
const humanDeadline = 24 * time.Hour

// wouldTakeHumanIdentity reports whether a caller's op would land on the
// operator's own agent.
//
// Split from exec so the decision is testable: on a zero-value Engine
// e.query() blocks forever rather than failing, so anything reachable only
// through the wrapper is effectively untested. See AGENTS.md.
//
// Two ways in, and both are closed. The nonce may RESOLVE to the human's row on
// a board where it already exists, and it may simply BE the mint credential on
// a board where it does not: pre-creating the row on a fresh daemon is the same
// takeover a day earlier. The (name, session_id) path needs no guard, because
// it refuses any agent that already holds a nonce and the human always does.
func (e *Engine) wouldTakeHumanIdentity(op *core.Op) bool {
	if op == nil || (op.Kind != core.OpRegister && op.Kind != core.OpResume) {
		return false
	}
	if op.Nonce == "" {
		return false
	}
	if op.Nonce == humanNonce() {
		return true
	}
	// The daemon's OWN reporting identity is reserved on the same terms.
	//
	// `dibs` registers lazily with the fixed nonce "system:dibs", which is a
	// constant in this repository and therefore public. An agent presenting it
	// is handed a rotated token and speaks as the thing that reports faults: a
	// trusted source, on a board where an agent reading "Dibs found a fault"
	// has no way to check who wrote it. Registering some OTHER name against
	// that nonce is worse in a quieter way, because the daemon's own later
	// registration then fails with E_NONCE_IN_USE and ReportFault logs and
	// drops the one-shot report, so the diagnostics go silent and nothing says
	// why.
	//
	// The guard existed and covered one identity. Two identities are minted by
	// this daemon and only one of them was reserved, which is the kind of gap
	// that comes from fixing an instance instead of a class. Found by a
	// pre-release review.
	if op.Nonce == dibsNonce() {
		return true
	}
	if h := e.humanIdentityLocked(); h != "" {
		if id, ok := e.state.Nonces[op.Nonce]; ok && id == h {
			return true
		}
	}
	return false
}

// refuseEmptyAdoption rejects an adoption whose source mailbox holds nothing.
//
// Split from exec so the decision can be tested without the loop, for the
// reason AGENTS.md gives about e.query on a zero-value Engine. Runs ON the loop,
// so it reads state directly.
func (e *Engine) refuseEmptyAdoption(from string) error {
	if from == "" {
		return nil // a missing target is the fold's error to report, with its own hint
	}
	if src := e.state.Agents[from]; src == nil {
		return nil
	}
	// Recover readable inbox mail and explicitly marked accepted debt shown
	// in task_queue/owed_work, never merely because a retained record exists.
	//
	// This counted any retained record, and a consumed terminal one is retained
	// for fifteen minutes after it is answered. So: notify an agent, let it
	// acknowledge, let it go dormant, adopt it. The check saw a record, the fold
	// moved and counted it, and the answer was ok:true with a positive message
	// count and "read them with inbox", into an inbox that shows nothing,
	// because Inbox excludes exactly those. The one thing the check exists to
	// prevent, reported as a rescue. Found by a pre-release review, which is the
	// second time this predicate has been not quite the right one.
	if e.state.RecoveryCount(from) > 0 {
		return nil
	}
	return &core.Error{
		Code: "E_NOTHING_TO_ADOPT",
		Msg:  "agent " + from + " has no mail to move",
		Hint: "nothing was changed. Adoption moves messages and records nothing else, " +
			"so there is no effect to be had here. If the row itself is the problem, " +
			"prune removes it; if you expected mail, check the id on the board",
	}
}

// adoptionSourceOf is the agent an approval would adopt from, or "".
//
// Reads the message being responded to, because the adoption is recorded on the
// REQUEST rather than on the response: an approval carries only the serial it
// answers. Runs on the loop, like the other ingress decisions beside it.
func (e *Engine) adoptionSourceOf(op *core.Op) string {
	if op.Disposition != "approve" {
		return ""
	}
	m, ok := e.state.Messages[op.MsgSerial]
	if !ok || m == nil {
		return ""
	}
	return m.Adopt
}

// refuseRetiredRequester rejects approving a request whose asker has gone.
//
// THE FALSE SUCCESS THIS ENDS. Apply notices that the requester is gone and
// marks the response undelivered, and then performs the effects anyway: the
// role is granted to a retired agent, and an adopted mailbox is moved INTO one
// whose token has been blanked and which cannot resume. So a coordinator
// approving a rescue moved the rescued mail somewhere nobody can ever read it,
// and was told it worked. The direct adoption path already refuses a closed or
// archived destination; only this route did not. Reproduced end to end by a
// pre-release review: request, sign off, approve, mail gone.
//
// At ingress, not in the fold. Refusing inside Apply would bind every approval
// already on disk, and skipping the effect there would make replay produce a
// different board from the one that was written.
func (e *Engine) refuseRetiredRequester(op *core.Op) error {
	if op.Disposition != "approve" {
		return nil
	}
	m, ok := e.state.Messages[op.MsgSerial]
	if !ok || m == nil {
		return nil // the fold reports a missing message, with its own hint
	}
	if m.Grant == "" && m.Adopt == "" {
		return nil // nothing is performed, so nothing lands anywhere
	}
	asker := e.state.Agents[m.From]
	if asker == nil {
		return &core.Error{
			Code: "E_NO_AGENT", Msg: "the agent that asked is gone",
			Hint: "nothing was changed. Its row has been removed, so there is nobody " +
				"for this to take effect on",
		}
	}
	if asker.Status == core.StatusClosed || asker.Status == core.StatusArchived {
		return &core.Error{
			Code: "E_AGENT_CLOSED",
			Msg:  "the agent that asked has retired (" + string(asker.Status) + ")",
			Hint: "nothing was changed. Approving would grant a role to an agent that " +
				"cannot act, or move a mailbox into one that cannot read: if a live " +
				"agent needs this, have it ask, and deny this one",
		}
	}
	return nil
}

// refuseNoOpRetitle rejects renaming a space to the name it already has.
//
// It assigned, emitted an event and advanced the serial unconditionally, so
// repeating the current topic appended an op that changed nothing. "An op is
// ledgered iff it changed replayable state" is one of the four rules here, and
// this broke it in the direction only somebody reading the ledger would notice.
//
// At ingress, because the alternative is a rule inside the fold, and a fold that
// refuses a same-value retitle refuses every one already on disk: the daemon
// would decline to replay its own history. That is the first bug class
// AGENTS.md lists, and I wrote it once tonight before moving it here.
//
// Refused rather than quietly skipped: a caller that believes it renamed
// something should be told it did not.
func (e *Engine) refuseNoOpRetitle(op *core.Op) error {
	if op.Kind != core.OpSpaceRetitle {
		return nil
	}
	ch := e.state.Spaces[op.Space]
	if ch == nil || ch.Topic != op.Text {
		return nil // missing space is the fold's error, with its own hint
	}
	return &core.Error{
		Code: "E_BAD_ARG", Msg: "the topic of " + ch.ID + " is already that",
		Hint: "nothing was changed. Read the space to see what it currently says",
	}
}

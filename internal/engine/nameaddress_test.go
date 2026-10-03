package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// nextNonce keeps every registration in this file a distinct agent.
var nextNonce atomic.Int64

// regFor registers an agent through the door production uses and acknowledges
// the board, so a send from it is not refused by the check_in gate.
//
// The nonce is unique per call rather than derived from the name: registering
// the same name twice is how a second row comes to CARRY that name while
// holding a suffixed id, and a repeated nonce would reattach to the first row
// instead, which is the setup silently not happening.
func regFor(t *testing.T, e *Engine, ctx context.Context, name string) (id, token string) {
	t.Helper()
	nonce := fmt.Sprintf("nonce-%s-%d", name, nextNonce.Add(1))
	res, err := e.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: name, AgentKind: core.KindPersistent,
		Nonce: nonce, NewToken: "tok-" + nonce,
	})
	if err != nil {
		t.Fatalf("setup: register %s: %v", name, err)
	}
	id, _ = res["agent_id"].(string)
	token, _ = res["token"].(string)
	if id == "" || token == "" {
		t.Fatalf("setup: register %s returned no id/token: %+v", name, res)
	}
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpAckBoard, Token: token}); err != nil {
		t.Fatalf("setup: check_in %s: %v", name, err)
	}
	return id, token
}

// rename renames an agent the way an agent does: update(name=…).
func rename(t *testing.T, e *Engine, ctx context.Context, token, to string) {
	t.Helper()
	res, err := e.Do(ctx, &core.Op{Kind: core.OpUpdate, Token: token, Name: to})
	if err != nil {
		t.Fatalf("setup: rename to %q: %v", to, err)
	}
	if got, _ := res["name"].(string); got != to {
		t.Fatalf("setup: rename reported name %q, want %q: %+v", got, to, res)
	}
}

// THE DEFECT, observed on a live board on 2026-09-26. An agent registered as
// `codex-primary` and was asked to rename itself `gpt-dibs`. Its row then read
// id: codex-primary, name: gpt-dibs, and a peer that did what the board invites
// and addressed `gpt-dibs` was told no such agent exists, while the only string
// that worked was the one the board no longer shows as its identity.
//
// Discovery and addressing disagreed, which is the whole bug: a rename is what
// an agent does when it CHANGES ROLE, and every peer that learns the new role
// learns an address that reaches nobody.
func TestAnAgentIsReachableByTheNameTheBoardShows(t *testing.T) {
	e, ctx, cancel := runningEngine(t)
	defer cancel()

	id, tok := regFor(t, e, ctx, "codex-primary")
	_, peerTok := regFor(t, e, ctx, "peer")
	rename(t, e, ctx, tok, "gpt-dibs")

	res, err := e.Do(ctx, &core.Op{
		Kind: core.OpSendMessage, Token: peerTok, To: "gpt-dibs",
		MsgType: core.MsgNotify, Body: "addressed by the name the board shows",
	})
	if err != nil {
		t.Fatalf("a peer addressing the name on the board was refused: %v; the board "+
			"publishes the name as the agent's identity, so the name has to be an address", err)
	}
	if inbox := e.state.Inbox(id); len(inbox) != 1 {
		t.Fatalf("the renamed agent holds %d messages, want 1: the send reported %+v",
			len(inbox), res)
	}
}

// And the id keeps working, forever, because it never stopped being the row's key.
//
// This is the half a remap would have put at risk, so it is asserted rather
// than assumed: mail already addressed to the old id, by a peer that learned it
// before the rename, must still arrive.
func TestTheOldIdStillAddressesARenamedAgent(t *testing.T) {
	e, ctx, cancel := runningEngine(t)
	defer cancel()

	id, tok := regFor(t, e, ctx, "codex-primary")
	_, peerTok := regFor(t, e, ctx, "peer")
	rename(t, e, ctx, tok, "gpt-dibs")

	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpSendMessage, Token: peerTok, To: id,
		MsgType: core.MsgNotify, Body: "addressed by the id learned before the rename",
	}); err != nil {
		t.Fatalf("the id stopped working after a rename: %v", err)
	}
	if inbox := e.state.Inbox(id); len(inbox) != 1 {
		t.Fatalf("mail addressed to the old id did not arrive: inbox=%d", len(inbox))
	}
}

// The LEDGER records the id, never the name.
//
// The same rule `to: "coordinator"` follows, for the same reason: a name moves,
// so an op that recorded one would be replayed into a delivery to whoever holds
// that name at replay time. Resolving at ingress and writing the id into the op
// keeps the fold untouched, which is what keeps state == fold(ledger) true.
func TestANameIsResolvedIntoTheOpBeforeItIsLedgered(t *testing.T) {
	e, ctx, cancel := runningEngine(t)
	defer cancel()

	id, tok := regFor(t, e, ctx, "codex-primary")
	_, peerTok := regFor(t, e, ctx, "peer")
	rename(t, e, ctx, tok, "gpt-dibs")

	op := &core.Op{
		Kind: core.OpSendMessage, Token: peerTok, To: "gpt-dibs",
		MsgType: core.MsgNotify, Body: "recorded by id",
	}
	if _, err := e.Do(ctx, op); err != nil {
		t.Fatalf("send by name: %v", err)
	}
	if op.To != id {
		t.Errorf("the op went into the ledger with to=%q, want the resolved id %q: a "+
			"ledger that records a name is a ledger whose replay can deliver elsewhere",
			op.To, id)
	}
}

// An id always wins outright, even when another agent carries that string as
// its NAME. An op that worked before this change works identically after it.
func TestAnExactIdIsNeverOvertakenByAnotherAgentsName(t *testing.T) {
	e, ctx, cancel := runningEngine(t)
	defer cancel()

	// `sol` registers and is joined by a second agent asking for the same name,
	// which register suffixes: sol-2, name sol. Then `sol` renames itself, so
	// the string "sol" is one row's id and another row's name.
	solID, solTok := regFor(t, e, ctx, "sol")
	otherID, _ := regFor(t, e, ctx, "sol")
	if otherID == solID {
		t.Fatalf("setup: the second registration took the first one's id %q", solID)
	}
	_, peerTok := regFor(t, e, ctx, "peer")
	rename(t, e, ctx, solTok, "ledger-surgeon")

	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpSendMessage, Token: peerTok, To: "sol",
		MsgType: core.MsgNotify, Body: "the id is the address",
	}); err != nil {
		t.Fatalf("send to an existing id was refused: %v", err)
	}
	if got := len(e.state.Inbox(solID)); got != 1 {
		t.Fatalf("mail addressed to the id %q reached it %d times, want 1: an id is the "+
			"address and a namesake must never overtake one", solID, got)
	}
	if got := len(e.state.Inbox(otherID)); got != 0 {
		t.Fatalf("mail addressed to the id %q was delivered to %q instead", solID, otherID)
	}
}

// A rename onto another agent's ID is refused, for the reason a rename onto
// another agent's NAME already is: it is a mail-redirection primitive.
//
// Without this an agent could take a name that resolves, by rule, to somebody
// else, and the board would show a name that addresses another row. That is the
// defect above with the arrow reversed, and it is worth refusing rather than
// warning about, because the agent is one call away from picking another name.
func TestRenamingOntoAnotherAgentsIdIsRefused(t *testing.T) {
	e, ctx, cancel := runningEngine(t)
	defer cancel()

	solID, solTok := regFor(t, e, ctx, "sol")
	_, otherTok := regFor(t, e, ctx, "worker")
	rename(t, e, ctx, solTok, "ledger-surgeon") // frees the string "sol" as a name

	_, err := e.Do(ctx, &core.Op{Kind: core.OpUpdate, Token: otherTok, Name: solID})
	if err == nil {
		t.Fatalf("an agent renamed itself to %q, which is another agent's address: "+
			"peers addressing that string reach the other row", solID)
	}
	var ce *core.Error
	if !errors.As(err, &ce) || ce.Code != "E_NAME_TAKEN" {
		t.Fatalf("err = %v, want E_NAME_TAKEN", err)
	}
	if ce.Hint == "" {
		t.Error("no hint: every error carries the corrective call")
	}
}

// AND A RETIRED AGENT'S ID, which is the case that decides whether the rename
// result is honest.
//
// An id resolves while its row exists at all, whatever its status: core.AgentRef
// is blind to it, because prune's targets and an archived agent's mail both
// depend on that. So a rename onto a closed agent's id would publish a label
// resolving to a mailbox nothing can be delivered to, and `update` would answer
// "peers may now address you as <name>" while that name reached a tombstone.
func TestRenamingOntoARetiredAgentsIdIsRefusedToo(t *testing.T) {
	e, ctx, cancel := runningEngine(t)
	defer cancel()

	goneID, goneTok := regFor(t, e, ctx, "sol")
	_, otherTok := regFor(t, e, ctx, "worker")
	rename(t, e, ctx, goneTok, "ledger-surgeon") // frees "sol" as a name
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpSignOff, Token: goneTok}); err != nil {
		t.Fatalf("setup: sign_off: %v", err)
	}
	onLoop(t, ctx, e, func(s *core.State) {
		if a := s.Agents[goneID]; a == nil || !a.Gone() {
			t.Fatalf("setup: %s is not retired, so this proves nothing", goneID)
		}
	})

	_, err := e.Do(ctx, &core.Op{Kind: core.OpUpdate, Token: otherTok, Name: goneID})
	var ce *core.Error
	if !errors.As(err, &ce) || ce.Code != "E_NAME_TAKEN" {
		t.Fatalf("err = %v, want E_NAME_TAKEN: %q still resolves to the retired row, so "+
			"the label would address a mailbox nothing can reach", err, goneID)
	}
}

// A name two live agents share is refused, not guessed.
//
// Dibs never picks a mailbox on the caller's behalf: the whole failure this
// board exists to prevent is a message that was accepted and read by the wrong
// agent. The refusal names the candidates so the caller can address one.
func TestAnAmbiguousNameIsRefusedAndNamesTheCandidates(t *testing.T) {
	s := core.NewState("n1", core.DefaultLimits())
	// Two live rows named "sol", and no row whose ID is "sol": reachable on a
	// board where the original `sol` was pruned out after its namesakes
	// registered, which is ordinary retention.
	for _, id := range []string{"sol-2", "sol-3"} {
		s.Agents[id] = &core.Agent{ID: id, Name: "sol", Status: core.StatusActive}
	}
	got, ambiguous := s.AgentRef("sol")
	if got != "" {
		t.Fatalf("AgentRef(sol) = %q: two live agents hold that name and picking one "+
			"delivers somebody's mail to the wrong agent", got)
	}
	if strings.Join(ambiguous, ",") != "sol-2,sol-3" {
		t.Fatalf("ambiguous = %v, want both candidates in a stable order", ambiguous)
	}
}

// And the refusal reaches a caller through the door a caller uses, rather than
// only being true of the pure rule above.
//
// The board is built by hand because the front door cannot produce it in three
// calls: register suffixes an id and refuses nothing, rename refuses a live
// peer's name, and it takes a purge of the original to leave two namesakes with
// no row holding the name as an id. The state is the one this repository has
// seen; what is under test is that e.Do refuses it and says who is who.
func TestAnAmbiguousNameIsRefusedAtTheDoorCallersUse(t *testing.T) {
	e, ctx, cancel := runningEngine(t)
	defer cancel()

	_, peerTok := regFor(t, e, ctx, "peer")
	onLoop(t, ctx, e, func(s *core.State) {
		for _, id := range []string{"sol-2", "sol-3"} {
			s.Agents[id] = &core.Agent{ID: id, Name: "sol", Status: core.StatusActive}
		}
	})

	_, err := e.Do(ctx, &core.Op{
		Kind: core.OpSendMessage, Token: peerTok, To: "sol",
		MsgType: core.MsgNotify, Body: "which sol?",
	})
	var ce *core.Error
	if !errors.As(err, &ce) || ce.Code != "E_AMBIGUOUS_AGENT" {
		t.Fatalf("err = %v, want E_AMBIGUOUS_AGENT: two live agents are called sol and "+
			"picking one delivers a message to the wrong agent", err)
	}
	for _, want := range []string{"sol-2", "sol-3"} {
		if !strings.Contains(ce.Hint, want) {
			t.Errorf("hint does not name %q, so the caller cannot correct the call: %s",
				want, ce.Hint)
		}
	}
	// AND NOTHING WAS LEDGERED. The refusal is at ingress, before Apply, which
	// is what keeps a refused address out of the record entirely.
	if got := len(e.state.Messages); got != 0 {
		t.Errorf("%d messages exist after a refused send", got)
	}
}

// A retired row never shadows the live agent that took its name over: the
// lesson resolveConfiguredAgentDecision learned at the role pin, applied to
// addressing, from the one function that now states it.
func TestARetiredRowDoesNotShadowALiveNamesake(t *testing.T) {
	s := core.NewState("n1", core.DefaultLimits())
	s.Agents["fleet-lead-0"] = &core.Agent{
		ID: "fleet-lead-0", Name: "fleet-lead", Status: core.StatusArchived,
	}
	s.Agents["fleet-lead-2"] = &core.Agent{
		ID: "fleet-lead-2", Name: "fleet-lead", Status: core.StatusActive,
	}
	if got, amb := s.AgentRef("fleet-lead"); got != "fleet-lead-2" {
		t.Fatalf("AgentRef = %q (ambiguous %v), want the live row: a retired one holds "+
			"the name only because the ledger refers to it", got, amb)
	}
}

// A name held only by retired rows still resolves, because the calls that take
// a retired agent's address are the ones that clean up after it.
func TestANameHeldOnlyByARetiredRowStillResolves(t *testing.T) {
	s := core.NewState("n1", core.DefaultLimits())
	s.Agents["debris-2"] = &core.Agent{ID: "debris-2", Name: "debris", Status: core.StatusClosed}
	if got, amb := s.AgentRef("debris"); got != "debris-2" {
		t.Fatalf("AgentRef = %q (ambiguous %v), want debris-2: prune's whole job is rows "+
			"in exactly this state", got, amb)
	}
}

// Nothing resolves to nothing, and the literal is left alone so the fold's own
// refusal, which names the nearest live agents, is the one the caller reads.
func TestAnUnknownReferenceIsLeftForTheFoldToExplain(t *testing.T) {
	e, ctx, cancel := runningEngine(t)
	defer cancel()

	_, peerTok := regFor(t, e, ctx, "peer")
	_, err := e.Do(ctx, &core.Op{
		Kind: core.OpSendMessage, Token: peerTok, To: "nobody-at-all",
		MsgType: core.MsgNotify, Body: "x",
	})
	var ce *core.Error
	if !errors.As(err, &ce) || ce.Code != "E_NO_AGENT" {
		t.Fatalf("err = %v, want the fold's E_NO_AGENT", err)
	}
	if !strings.Contains(ce.Hint, "peer") {
		t.Errorf("hint = %q: it should still name the live agents", ce.Hint)
	}
}

// EVERY SIBLING, not just send. A rule applied at one call site and not its
// siblings is this repository's most expensive recurring bug, and `to` names an
// agent on nine ops. Each one is driven through e.Do, which is the door the
// board, the CLI and every MCP tool go through.
func TestEveryOpThatAddressesAnAgentTakesAName(t *testing.T) {
	for _, tc := range []struct {
		what string
		op   func(target, actorTok string) *core.Op
	}{
		{"grant_role", func(target, _ string) *core.Op {
			return &core.Op{Kind: core.OpGrantRole, To: target, Mode: core.RoleCoordinator}
		}},
		{"prune", func(target, _ string) *core.Op {
			return &core.Op{Kind: core.OpPrune, To: target}
		}},
		{"force_release", func(target, tok string) *core.Op {
			return &core.Op{Kind: core.OpForceRelease, Token: tok, To: target, Path: "/tmp/x"}
		}},
		{"adopt_agent", func(target, tok string) *core.Op {
			return &core.Op{Kind: core.OpAdoptAgent, Token: tok, To: target}
		}},
		{"admit", func(target, tok string) *core.Op {
			return &core.Op{Kind: core.OpSpaceAdmit, Token: tok, Space: "s1", To: target}
		}},
		{"evict", func(target, tok string) *core.Op {
			return &core.Op{Kind: core.OpSpaceEvict, Token: tok, Space: "s1", To: target}
		}},
		{"merge_agents", func(target, _ string) *core.Op {
			return &core.Op{Kind: core.OpMergeAgents, To: target, MergeInto: "actor"}
		}},
		// THE FOUR THAT ARRIVED WHILE THIS BRANCH SAT, and the reason the
		// exemption list is inverted rather than an allow-list. relocate,
		// relocate_by_human, grant_permission and revoke_permission all name an
		// agent in `to` and all landed on main after the resolution step was
		// written. None of them needed an edit: a list of the ops that DO
		// address an agent would have missed every one, silently, and the
		// symptom would have been an operator naming a renamed agent and moving
		// nobody.
		{"relocate", func(target, tok string) *core.Op {
			return &core.Op{Kind: core.OpRelocate, Token: tok, To: target, Mode: "dev"}
		}},
		{"relocate_by_human", func(target, _ string) *core.Op {
			return &core.Op{Kind: core.OpRelocateByHuman, To: target, Mode: "dev"}
		}},
		{"grant_permission", func(target, _ string) *core.Op {
			return &core.Op{Kind: core.OpGrantPermission, To: target, Mode: "relocate"}
		}},
		{"revoke_permission", func(target, _ string) *core.Op {
			return &core.Op{Kind: core.OpRevokePermission, To: target, Mode: "relocate"}
		}},
	} {
		t.Run(tc.what, func(t *testing.T) {
			e, ctx, cancel := runningEngine(t)
			defer cancel()
			id, tok := regFor(t, e, ctx, "codex-primary")
			_, actorTok := regFor(t, e, ctx, "actor")
			rename(t, e, ctx, tok, "gpt-dibs")
			_ = tok

			op := tc.op("gpt-dibs", actorTok)
			// The op may well be refused for a reason of its own (no such
			// claim, no such space, not authorised). What must not happen is
			// that the NAME survives into the fold unresolved: that is the
			// resolution step missing at this call site.
			_, _ = e.Do(ctx, op)
			if op.To != id && op.MergeInto != id {
				t.Errorf("%s went into the fold with to=%q merge_into=%q: the name was "+
					"never resolved, so this call site addresses nobody", tc.what, op.To, op.MergeInto)
			}
		})
	}
}

// AND THE READS, which never pass the ingress step at all.
//
// all_mail's census is what a coordinator asks about an agent it has just
// watched change role, and `agent` is matched against a message's `to`, which is
// an id. A name therefore selected nothing and the answer was zeros: a mailbox
// reported empty when it was not, which is the failure mode this repository
// treats as worse than an error.
func TestTheMailboxCensusTakesAName(t *testing.T) {
	e, ctx, cancel := runningEngine(t)
	defer cancel()

	id, tok := regFor(t, e, ctx, "codex-primary")
	_, leadTok := regFor(t, e, ctx, "lead")
	rename(t, e, ctx, tok, "gpt-dibs")
	if _, err := e.GrantRole(ctx, "lead", core.RoleCoordinator); err != nil {
		t.Fatalf("setup: grant: %v", err)
	}
	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpSendMessage, Token: leadTok, To: id,
		MsgType: core.MsgNotify, Body: "one message",
	}); err != nil {
		t.Fatalf("setup: send: %v", err)
	}

	res, err := e.AllMail(ctx, leadTok, true, "gpt-dibs")
	if err != nil {
		t.Fatalf("census by name: %v", err)
	}
	rows, _ := res["census"].([]MailboxCensus)
	if len(rows) != 1 || rows[0].Agent != id || rows[0].Messages != 1 {
		t.Fatalf("census = %+v, want one row for %s holding 1 message: a name selected "+
			"nothing and the coordinator was told the mailbox was empty", rows, id)
	}
}

// AND NOT THE FIELDS THAT NAME SOMETHING ELSE. `to` on merge_spaces is a SPACE,
// so an agent whose name happens to match a space id must not capture it.
func TestASpaceReferenceIsNotResolvedAsAnAgent(t *testing.T) {
	e, ctx, cancel := runningEngine(t)
	defer cancel()

	_, tok := regFor(t, e, ctx, "codex-primary")
	rename(t, e, ctx, tok, "attic") // an agent now NAMED like the space below

	op := &core.Op{Kind: core.OpSpaceMerge, Token: tok, Space: "s1", To: "attic"}
	_, _ = e.Do(ctx, op)
	if op.To != "attic" {
		t.Errorf("merge_spaces went to the fold with to=%q: `to` is a space id here, and "+
			"rewriting it into an agent id sends the merge somewhere that is not a space",
			op.To)
	}
}

// THE PERSON'S ADDRESS IS A ROLE, AND A ROLE WINS OVER A NAME.
//
// `to: "human"` reaches the person at the keyboard, minted on demand by
// prepareHumanRecipient. That runs later in exec than name resolution, and
// nothing stops a local agent calling itself `human`: the person's own row is
// named after the OS user, and only invitations reserve the word. So name
// resolution, running first, rewrote "human" into that agent's id and the
// person's mail went to an agent. The same is already true of "coordinator",
// which is why that one is resolved before names; this is its sibling.
func TestAnAgentNamedHumanDoesNotCaptureThePersonsMail(t *testing.T) {
	e, ctx, cancel := runningEngine(t)
	defer cancel()

	impostorID, impostorTok := regFor(t, e, ctx, "worker")
	_, senderTok := regFor(t, e, ctx, "sender")
	rename(t, e, ctx, impostorTok, "human")

	op := &core.Op{
		Kind: core.OpSendMessage, Token: senderTok, To: "human",
		MsgType: core.MsgNotify, Body: "for the person",
	}
	if _, err := e.Do(ctx, op); err != nil {
		t.Fatalf("send to the human role: %v", err)
	}
	if op.To == impostorID {
		t.Fatalf("send(to: \"human\") was delivered to %s, an agent that named itself "+
			"human: the person's address is a role and must win over a label", impostorID)
	}
	if got := len(e.state.Inbox(impostorID)); got != 0 {
		t.Fatalf("the agent named human received %d messages meant for the person", got)
	}
}

// queue_lock is a door main added beside exec rather than through it: the
// engine builds the permission op itself, so the resolution exec performs
// never ran and a coordinator naming the agent the board shows was told
// "unknown queue owner". Same bug class as the rest of this file, found on
// the newest sibling, which is where it always is.
func TestQueueLockTakesAName(t *testing.T) {
	e, ctx, cancel := runningEngine(t)
	defer cancel()

	id, tok := regFor(t, e, ctx, "codex-primary")
	_, leadTok := regFor(t, e, ctx, "lead")
	rename(t, e, ctx, tok, "gpt-dibs")
	if _, err := e.GrantRole(ctx, "lead", core.RoleCoordinator); err != nil {
		t.Fatalf("setup: grant: %v", err)
	}
	if e.state.Agents[id].HasPermission(core.PermQueueOrderLock) {
		t.Fatal("setup: the agent already held queue_order_lock")
	}

	if _, err := e.SetQueueOrderLock(ctx, leadTok, "gpt-dibs", 0, true); err != nil {
		t.Fatalf("queue_lock refused the name the board shows: %v", err)
	}
	if !e.state.Agents[id].HasPermission(core.PermQueueOrderLock) {
		t.Fatal("queue_lock by name succeeded but the renamed agent does not hold the lock")
	}
}

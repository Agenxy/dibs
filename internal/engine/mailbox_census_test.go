package engine

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// censusBoard registers a coordinator, a member, and a dormant agent
// ("stranded") holding two messages (one question still waiting on an answer,
// one notify), and returns the first two tokens.
func censusBoard(t *testing.T, ctx context.Context, e *Engine) (coordinator, member string) {
	t.Helper()
	// Each with a session of its own, as a bridge would have stamped: an
	// agent with no session the board can record is not distinguishable
	// from one the coordinator minted, and adoption onto it is refused.
	reg := func(name string) string {
		res, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: name, SessionID: "session-" + name})
		if err != nil {
			t.Fatalf("setup: register %s: %v", name, err)
		}
		tok, _ := res["token"].(string)
		if _, err := e.Do(ctx, &core.Op{Kind: core.OpAckBoard, Token: tok}); err != nil {
			t.Fatalf("setup: ack %s: %v", name, err)
		}
		return tok
	}
	coordinator, member = reg("coord"), reg("member")
	reg("stranded")
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpGrantRole, To: "coord", Mode: core.RoleCoordinator}); err != nil {
		t.Fatalf("setup: grant: %v", err)
	}
	for _, m := range []struct {
		typ, body string
	}{{core.MsgQuestion, "still waiting?"}, {core.MsgNotify, "fyi"}} {
		if _, err := e.Do(ctx, &core.Op{
			Kind: core.OpSendMessage, Token: member, To: "stranded", MsgType: m.typ, Body: m.body,
		}); err != nil {
			t.Fatalf("setup: send: %v", err)
		}
	}
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpSweep, StaleAgents: []string{"stranded"}}); err != nil {
		t.Fatalf("setup: sweep: %v", err)
	}
	return coordinator, member
}

// A coordinator can count a mailbox without reading it.
//
// Consolidating stranded rows is a coordinator's job, and to place a mailbox
// it needs one fact: is there anything in it, and is anyone still waiting on
// an answer. The only door to that fact was all_mail, which refused, so a
// coordinator approved a consolidation while telling the requester it could
// not check. Two of the three mailboxes held nothing. Issue #77.
func TestACoordinatorCanCountAMailboxWithoutReadingIt(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)
	coordinator, member := censusBoard(t, ctx, e)

	res, err := e.AllMail(ctx, coordinator, true, "stranded")
	if err != nil {
		t.Fatalf("census by a coordinator: %v", err)
	}
	rows, _ := res["census"].([]MailboxCensus)
	if len(rows) != 1 || rows[0].Agent != "stranded" {
		t.Fatalf("census = %+v, want one row for the named agent", rows)
	}
	row := rows[0]
	if row.Messages != 2 || row.AwaitingAnswer != 1 || row.Unread != 2 ||
		row.ByType[core.MsgQuestion] != 1 || row.ByType[core.MsgNotify] != 1 ||
		len(row.Senders) != 1 || row.Senders[0] != "member" || row.Oldest.IsZero() {
		t.Errorf("row = %+v, want 2 messages, 1 awaiting an answer, 2 unread, one sender", row)
	}
	if _, hasBodies := res["messages"]; hasBodies {
		t.Error("a census returned messages: custody needs counts, and only admin reads bodies")
	}
	// And the whole board when no agent is named: retired rows excluded, the
	// empty ones reported with zeros.
	res, err = e.AllMail(ctx, coordinator, true, "")
	if err != nil {
		t.Fatalf("board census: %v", err)
	}
	rows, _ = res["census"].([]MailboxCensus)
	if len(rows) != 3 {
		t.Errorf("board census = %d rows, want 3 (every live agent, empty ones included)", len(rows))
	}

	// A member is refused the census; a coordinator is still refused bodies.
	var ce *core.Error
	if _, err := e.AllMail(ctx, member, true, ""); !errors.As(err, &ce) || ce.Code != "E_NOT_COORDINATOR" {
		t.Errorf("a member's census: %v, want E_NOT_COORDINATOR", err)
	}
	if _, err := e.AllMail(ctx, coordinator, false, ""); !errors.As(err, &ce) || ce.Code != "E_NOT_ADMIN" {
		t.Errorf("a coordinator's all_mail without census: %v, want E_NOT_ADMIN: counts are not bodies", err)
	}
}

// A coordinator may say where a mailbox goes; it may not make itself the
// reader of one. Onto a third party is custody; onto itself is contents, and
// that is the human's call.
func TestACoordinatorAdoptingOntoItselfIsTheHumansCall(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)
	coordinator, _ := censusBoard(t, ctx, e)

	var ce *core.Error
	_, err := e.Do(ctx, &core.Op{Kind: core.OpAdoptAgent, Token: coordinator, To: "stranded"})
	if !errors.As(err, &ce) || ce.Code != "E_NOT_PERMITTED" {
		t.Fatalf("coordinator adopting onto itself: %v, want E_NOT_PERMITTED: that is the "+
			"coordinator granting itself read access to another agent's mail", err)
	}
	if !strings.Contains(ce.Hint, "census") || !strings.Contains(ce.Hint, "into") {
		t.Errorf("hint = %q, want it to offer the census and `into`", ce.Hint)
	}
	// Explicitly naming itself is the same move.
	_, err = e.Do(ctx, &core.Op{Kind: core.OpAdoptAgent, Token: coordinator, To: "stranded", Space: "coord"})
	if !errors.As(err, &ce) || ce.Code != "E_NOT_PERMITTED" {
		t.Errorf("coordinator adopting onto itself by name: %v, want E_NOT_PERMITTED", err)
	}
	// Onto a third party: custody only, and the consolidation the role is for.
	res, err := e.Do(ctx, &core.Op{Kind: core.OpAdoptAgent, Token: coordinator, To: "stranded", Space: "member"})
	if err != nil {
		t.Fatalf("coordinator adopting onto a third party: %v, want it to succeed", err)
	}
	if moved, _ := res["messages"].(int); moved != 2 {
		t.Errorf("adopted %v, want the 2 messages moved onto member", res)
	}
}

// An admin reads every mailbox already, so redirecting one onto itself grants
// it nothing it lacked; the rule is for the plain coordinator.
func TestAnAdminMayAdoptOntoItself(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)
	coordinator, _ := censusBoard(t, ctx, e)
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpGrantRole, To: "coord", Mode: core.RoleAdmin}); err != nil {
		t.Fatalf("setup: promote: %v", err)
	}
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpAdoptAgent, Token: coordinator, To: "stranded"}); err != nil {
		t.Errorf("admin adopting onto itself: %v, want it to succeed", err)
	}
}

// The same move through the other door: a coordinator sends ITSELF a request
// carrying `adopt`, then approves it. The direct route refuses that
// (TestACoordinatorAdoptingOntoItselfIsTheHumansCall); the request route ran
// only the retired-requester and empty-mailbox checks, so the coordinator
// became the reader of the stranded mailbox with no human involved. Two
// routes to one effect, and the rule on one of them: found by the
// pre-release review.
func TestACoordinatorCannotApproveItsOwnAdoptionRequest(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)
	coordinator, member := censusBoard(t, ctx, e)

	sent, err := e.Do(ctx, &core.Op{
		Kind: core.OpSendMessage, Token: coordinator, To: "coord",
		MsgType: core.MsgRequest, Body: "consolidating", Adopt: "stranded",
	})
	if err != nil {
		t.Fatalf("setup: a coordinator's request to itself: %v", err)
	}
	var ce *core.Error
	_, err = e.Do(ctx, &core.Op{
		Kind: core.OpRespond, Token: coordinator, MsgSerial: sent["msg_serial"].(uint64),
		Disposition: "approve",
	})
	if !errors.As(err, &ce) || ce.Code != "E_NOT_PERMITTED" {
		t.Fatalf("coordinator approving its own adoption request: %v, want E_NOT_PERMITTED: "+
			"that is the coordinator making itself the reader of another agent's mail", err)
	}
	if got := st.Messages[sent["msg_serial"].(uint64)]; got == nil || got.Terminal() {
		t.Errorf("the refused request should still be pending, got %+v", got)
	}
	// Somebody else asking, and the coordinator approving, is the consolidation
	// the role exists for: the ASKER becomes the reader, not the coordinator.
	sent, err = e.Do(ctx, &core.Op{
		Kind: core.OpSendMessage, Token: member, To: "coord",
		MsgType: core.MsgRequest, Body: "I will take stranded's mail", Adopt: "stranded",
	})
	if err != nil {
		t.Fatal("setup:", err)
	}
	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpRespond, Token: coordinator, MsgSerial: sent["msg_serial"].(uint64),
		Disposition: "approve",
	}); err != nil {
		t.Fatalf("coordinator approving a member's adoption request: %v, want it to succeed", err)
	}
}

// `all_mail(agent: x)` reads ONE mailbox for an admin, as the schema says.
//
// The argument selected a mailbox for the census and was never read on the
// admin path, so an admin asking for one worker's mail was handed every
// message on the board: a filter advertised and silently ignored, which is
// the failure AGENTS.md names under "a parameter you declare but never
// read". Round five of the pre-release review.
func TestAnAdminReadingOneMailboxGetsThatMailboxOnly(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)
	coordinator, member := censusBoard(t, ctx, e)
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpGrantRole, To: "coord", Mode: core.RoleAdmin}); err != nil {
		t.Fatal("setup:", err)
	}
	// Two mailboxes hold mail: stranded's (from the fixture) and coord's.
	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpSendMessage, Token: member, To: "coord", MsgType: core.MsgNotify, Body: "for you",
	}); err != nil {
		t.Fatal("setup:", err)
	}

	res, err := e.AllMail(ctx, coordinator, false, "stranded")
	if err != nil {
		t.Fatal(err)
	}
	msgs, _ := res["messages"].([]*core.Message)
	if len(msgs) != 2 {
		t.Fatalf("all_mail(agent: stranded) returned %d messages, want the 2 in that mailbox", len(msgs))
	}
	for _, m := range msgs {
		if m.To != "stranded" {
			t.Errorf("a message to %q came back from a read of stranded's mailbox", m.To)
		}
	}
}

package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// The nudge on every tool result must not read the same at five minutes as at
// five hours.
//
// This is the failure that produced it, and it is worth stating plainly because
// it looks like an agent problem and is not. The `waiting` line is the most
// reliable delivery channel Dibs has: it rides on every authenticated write, it
// needs no hook and no plugin, and it cannot be misrouted. During one release
// session it fired on something like forty consecutive tool calls, correctly,
// with one message unread the whole time. The agent read it, deferred it, and
// went on working, and the operator found the unread mail.
//
// The line was doing everything it was designed to do. It said the same eleven
// words every time, so after a few turns it stopped being read at all: not
// disobedience, habituation, the eye learning a shape and skipping it. The
// project had already written this down about the hook digest — "identical at
// one unread and at twenty, so an agent in a loop habituates within a few
// turns" — and the same sentence was true of this line and nobody noticed,
// because the surface that reports the problem was the surface with the
// problem.
//
// So: past the floor the line states an age, which makes it different text on
// every call AND gives the agent a fact it can weigh, since five minutes and
// five hours deserve different answers. Below the floor it stays quiet, because
// spending a changing sentence on mail that arrived ninety seconds ago is how
// the line went blind in the first place.
func TestTheWaitingNudgeAgesAndDoesNotRepeatItself(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	mk := func(name, nonce string) string {
		r, err := e.Do(ctx, &core.Op{
			Kind: core.OpRegister, Name: name, AgentKind: core.KindPersistent, Nonce: nonce,
		})
		if err != nil {
			t.Fatal("setup:", err)
		}
		tok, _ := r["token"].(string)
		return tok
	}
	senderTok := mk("sender", "n-s")
	mk("receiver", "n-r")

	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpSendMessage, Token: senderTok, To: "receiver",
		MsgType: core.MsgQuestion, Body: "does the line change",
	}); err != nil {
		t.Fatal("setup:", err)
	}

	// Assert the setup, or every check below passes against an empty mailbox:
	// "" contains no age and never differs from "", so this test would be
	// green with no mail at all. Three false alarms in this repository came
	// from a probe nobody checked the setup of.
	sent := time.Now()
	if base := e.waiting("receiver", sent); base == "" {
		t.Fatal("setup: no waiting line with one unread message, so the " +
			"comparisons below would all be between empty strings")
	}

	fresh := e.waiting("receiver", sent.Add(WaitingAgeFloor-time.Minute))
	if strings.Contains(fresh, "waiting") {
		t.Errorf("mail younger than the floor already reports an age: %q. Novelty is "+
			"the resource this line spends; spending it on mail that just arrived is "+
			"what made the old line invisible", fresh)
	}

	// The point of the whole change: two different ages, two different lines.
	at30 := e.waiting("receiver", sent.Add(30*time.Minute))
	at3h := e.waiting("receiver", sent.Add(3*time.Hour))
	if at30 == fresh {
		t.Errorf("after 30 minutes the line is unchanged from fresh (%q). An agent "+
			"that has seen this sentence forty times will not read it the "+
			"forty-first, which is the failure this exists to prevent", at30)
	}
	if at30 == at3h {
		t.Errorf("30 minutes and 3 hours produce the same line (%q). The age has to "+
			"be IN the text: a boolean 'old' flag flips once and then goes back to "+
			"being the same sentence forever", at30)
	}
	if !strings.Contains(at30, "30m") {
		t.Errorf("waiting line at 30 minutes = %q, want it to name the age. The agent "+
			"cannot triage what it cannot see, and 'you have mail' is the same "+
			"sentence whether it is minutes or days old", at30)
	}
	if !strings.Contains(at3h, "3h") {
		t.Errorf("waiting line at 3 hours = %q, want it to name the age in hours", at3h)
	}

	// Still counts-only. The age is a fact ABOUT the mail, not any of its
	// content, and the sibling property test that guards every nudge surface
	// against leaking a body must keep passing.
	if strings.Contains(at3h, "does the line change") {
		t.Errorf("the aged line carries the message body: %q", at3h)
	}

	// The hook digest is the OTHER surface with this problem, and it is the one
	// the operator watched fail: a SessionStart line that read the same on the
	// fortieth turn as the first. Its own comment had already diagnosed
	// habituation ("measured on the author of this function, who read two
	// notices and went on being told about them for hours") and then left the
	// text unchanging. Both surfaces or neither: fixing the one an agent sees
	// most and leaving the one a human sees is how a fix looks complete and is
	// not.
	digestFresh := hookDigest("receiver", e.pendingMail("receiver", sent), nil, nil)
	digestOld := hookDigest("receiver", e.pendingMail("receiver", sent.Add(3*time.Hour)), nil, nil)
	if digestFresh == digestOld {
		t.Errorf("the hook digest is identical at zero and three hours:\n%s", digestOld)
	}
	if !strings.Contains(digestOld, "3h") {
		t.Errorf("hook digest after 3 hours does not name the age:\n%s", digestOld)
	}
	if strings.Contains(digestOld, "does the line change") {
		t.Errorf("the aged digest carries the message body:\n%s", digestOld)
	}

	// And silence when there is nothing waiting: an age is only ever a
	// modifier on a real nudge, never a nudge of its own.
	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: "bystander",
		AgentKind: core.KindPersistent, Nonce: "n-b",
	}); err != nil {
		t.Fatal("setup:", err)
	}
	if w := e.waiting("bystander", sent.Add(3*time.Hour)); w != "" {
		t.Errorf("an agent with no mail is nudged anyway: %q", w)
	}
}

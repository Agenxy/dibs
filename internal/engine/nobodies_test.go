package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// No surface that a HOST may attach to a human's turn carries a message body.
//
// This has now been the same bug three times, in three different channels, and
// each was reported by the operator watching their own prompt box fill with
// mail addressed to an agent:
//
//   - the wake digest, which listed each message with its text;
//   - `dibs://inbox`, which returned the whole mailbox, because a resource is
//     application-controlled and the host decides who reads it;
//   - the `waiting` line, which was always counts-only and is included here so
//     it stays that way.
//
// The rule is one sentence: these surfaces say WHO is waiting and WHAT KIND,
// never what was said. The content is fetched with `inbox` or `read_mail`,
// which are token-authenticated and answer down the connection the agent
// authenticated on, so one extra call buys back the confidentiality claim.
//
// A property test rather than three example tests, because the failure keeps
// arriving through a channel nobody thought of. Anything new that composes a
// nudge should be added below.
func TestNoWakeSurfaceLeaksAMessageBody(t *testing.T) {
	const secret = "SENSITIVE-BODY-NOBODY-ELSE-MAY-READ"

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
		MsgType: core.MsgQuestion, Body: secret,
	}); err != nil {
		t.Fatal("setup:", err)
	}

	mail := e.pendingMail("receiver")
	if len(mail) == 0 {
		t.Fatal("setup: no pending mail, so this test would pass vacuously")
	}

	surfaces := map[string]string{
		"the wake digest (hook additionalContext)":    hookDigest("receiver", mail, nil, nil),
		"the human notice (hook systemMessage)":       humanNotice("receiver", mail, nil, nil),
		"the waiting line (every tool result)":        e.waiting("receiver"),
		"pendingMail (the lines both are built from)": strings.Join(mail, "\n"),
	}
	for name, text := range surfaces {
		if strings.Contains(text, secret) {
			t.Errorf("%s carries the message body. A host may attach this to the "+
				"operator's next turn, which puts one agent's private mail in front "+
				"of whoever is at the keyboard:\n%s", name, text)
		}
		// And it still has to be a usable nudge, or the fix has traded a
		// disclosure for a silence.
		//
		// "Usable" differs by surface, deliberately. The digest names the sender
		// and the serial so an agent can fetch the right message; the `waiting`
		// line is one clause appended to every authenticated result and names
		// only the count and the call, because repeating senders on every write
		// would cost more than it tells. Both are actionable; neither needs the
		// text.
		if text == "" {
			continue
		}
		actionable := false
		for _, cue := range []string{"sender", "inbox", "read_mail", "board"} {
			if strings.Contains(text, cue) {
				actionable = true
			}
		}
		if !actionable {
			t.Errorf("%s names neither who is waiting nor the call that reads them, "+
				"so it is an alarm rather than a nudge:\n%s", name, text)
		}
	}
	_ = time.Now
}

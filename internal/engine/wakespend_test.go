package engine

import (
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// Typing must not spend the wake that the end of the turn needs.
//
// THE BUG THIS CATCHES, found by a pre-release review. HookPoll computed
// freshness by calling freshForWake, which RECORDED the announcement, and only
// afterwards asked deliverToModel whether this event may carry mail at all.
// UserPromptSubmit may not, deliberately: its additionalContext is attached to
// the person's own prompt, so delivering there would make the human the
// trigger. So the ordinary sequence a harness sends, UserPromptSubmit then
// Stop, spent the wake on the event that cannot use it and left nothing for the
// one that can. The agent was never told, and every call returned success.
//
// hook_poll takes no token by design, so this was also reachable against
// somebody else: naming a victim's session id spent that agent's wake without
// reading a word of its mail. SECURITY.md says in as many words that the
// token-less path must not consume or advance anything.
func TestTypingDoesNotSpendTheWakeThatStopNeeds(t *testing.T) {
	e, ctx, cancel := runningEngine(t)
	defer cancel()

	register := func(name, nonce, session string) string {
		t.Helper()
		res, err := e.Do(ctx, &core.Op{
			Kind: core.OpRegister, Name: name, AgentKind: core.KindPersistent,
			Nonce: nonce, SessionID: session, NewToken: "tok-" + name,
		})
		if err != nil {
			t.Fatalf("register %s: %v", name, err)
		}
		tok, _ := res["token"].(string)
		return tok
	}
	sender := register("sender", "sender-nonce", "sender-session")
	register("worker", "worker-nonce", "worker-session")

	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpSendMessage, Token: sender, To: "worker",
		MsgType: core.MsgNotify, Body: "something happened",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}

	// The person types. Nothing may be delivered here.
	typed, err := e.HookPoll(ctx, "worker-session", "UserPromptSubmit", "", false)
	if err != nil {
		t.Fatalf("UserPromptSubmit: %v", err)
	}
	if typed["hookSpecificOutput"] != nil {
		t.Fatalf("mail was delivered on the human's own prompt: %v", typed)
	}

	// The turn ends. THIS is the event the wake exists for.
	stopped, err := e.HookPoll(ctx, "worker-session", "Stop", "", false)
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if stopped["hookSpecificOutput"] == nil {
		t.Fatalf("Stop delivered nothing, because typing had already spent the "+
			"freshness: the agent has mail and was never told. Got %v", stopped)
	}
}

// A poll that DELIVERS NOTHING must record nothing.
//
// Narrower than it first looks, and the name says so now. hook_poll takes no
// token, so a caller naming somebody else's session and claiming an event that
// CAN deliver is handed that agent's digest and spends its wake. That is a real
// property of the design and SECURITY.md states it: a session id is a
// capability on this path.
//
// What this pins is the half that is fixable: an event that cannot deliver must
// not spend anything either. The first version of this test used exactly such
// an event and called it proof against the whole attack, which it never was:
// UserPromptSubmit is hard-coded never to deliver, so it never reached
// markWoken, and a spoofed `Stop` did. Found by the review that had already
// found the bug once.
func TestAPollThatDeliversNothingSpendsNothing(t *testing.T) {
	e, ctx, cancel := runningEngine(t)
	defer cancel()

	res, err := e.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: "sender", AgentKind: core.KindPersistent,
		Nonce: "s-nonce", SessionID: "s-session", NewToken: "tok-sender",
	})
	if err != nil {
		t.Fatal(err)
	}
	sender, _ := res["token"].(string)
	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: "victim", AgentKind: core.KindPersistent,
		Nonce: "v-nonce", SessionID: "victim-session", NewToken: "tok-victim",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpSendMessage, Token: sender, To: "victim",
		MsgType: core.MsgNotify, Body: "for the victim only",
	}); err != nil {
		t.Fatal(err)
	}

	// A stranger polls against the victim's session on an event that cannot deliver.
	if _, err := e.HookPoll(ctx, "victim-session", "UserPromptSubmit", "", false); err != nil {
		t.Fatalf("poll: %v", err)
	}

	// The victim's own turn-end must still wake it.
	stopped, err := e.HookPoll(ctx, "victim-session", "Stop", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if stopped["hookSpecificOutput"] == nil {
		t.Error("a poll by somebody else consumed the victim's wake: its mail is " +
			"delivered to nobody and the board reports success to everyone")
	}
}

package engine

import (
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// Mail arriving while an agent is "recently in touch" is not thrown away.
//
// maybeWake short-circuits when the recipient called Dibs inside the wake
// cooldown, on the reasoning that it "is genuinely working and will see this at
// its own turn boundary". That is true only where a turn boundary REACHES Dibs.
// An agent whose harness sends no lifecycle hooks has none: nothing ever marks
// its turn ended, recency decays into silence, and because maybeWake fires once
// per event with nothing retrying, the message's only delivery attempt is spent
// on the assumption.
//
// Measured on a live board. A question went to an active codex agent 40 seconds
// after its last call, inside the 90-second window. No wake was attempted then
// or ever, and the daemon's log showed that harness had never delivered a single
// lifecycle hook: it runs under the desktop app, which does not read the CLI's
// hooks file. Every Codex desktop agent on that board was in the same position.
//
// So the window has to defer rather than decide. This asserts the timer is
// armed, which is the whole difference between "delivered late" and "never".
func TestRecentContactDefersTheWakeRatherThanDroppingIt(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	e.SetWakeCommands(map[string]WakeCommand{
		"codex": {Argv: []string{"/bin/echo", "{message}"}, Cooldown: time.Hour},
	})
	l := &core.Agent{
		ID: "busy", Name: "busy", Status: core.StatusActive,
		SessionID: "01a0696b-8446-7821-a992-9dc7f6a43a25",
		Agent:     &core.AgentInfo{Harness: "Codex", CWD: "/work"},
		Slots:     map[string]core.Slot{},
	}
	st.Agents["busy"] = l
	// Called Dibs a moment ago: inside the window, by a mile.
	e.seen["busy"] = time.Now()
	if !e.recentlyInTouch(l) {
		t.Fatal("setup: the agent does not read as recently in touch, so this " +
			"exercises the wrong branch entirely")
	}

	e.maybeWake(core.Event{
		Type: "message.sent", Agent: "asker", To: "busy",
		Data: map[string]any{"msg_type": core.MsgQuestion, "from": "asker"},
	})

	e.wakers.mu.Lock()
	timer := e.wakers.deferred["busy"]
	e.wakers.mu.Unlock()
	if timer == nil {
		t.Error("no re-check was armed. maybeWake fires once per event and nothing " +
			"else retries, so this message's only delivery attempt was spent on the " +
			"assumption that a turn boundary will carry it. For a harness that " +
			"sends no lifecycle hooks there is no turn boundary, and the mail is " +
			"never delivered by anything")
	}
}

// And the re-check re-arms while somebody is still blocked.
//
// Deferring once only moves the failure one window later: an agent that called
// Dibs again in the meantime consumes the retry and the message is stranded
// exactly as before. The loop ends when the mail does, not when the agent
// happens to be idle at the right instant.
func TestTheRecheckReArmsWhileTheAgentStaysBusy(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	e.SetWakeCommands(map[string]WakeCommand{
		"codex": {Argv: []string{"/bin/echo", "{message}"}, Cooldown: time.Hour},
	})
	mk := func(name, nonce string) string {
		r, _, err := st.Apply(&core.Op{
			Kind: core.OpRegister, Name: name, AgentKind: core.KindPersistent,
			Nonce: nonce, NewToken: "tok-" + name,
		}, time.Now())
		if err != nil {
			t.Fatal("setup:", err)
		}
		tok, _ := r["token"].(string)
		return tok
	}
	sender := mk("asker", "n-a")
	mk("busy", "n-b")
	l := st.Agents["busy"]
	l.Agent = &core.AgentInfo{Harness: "Codex", CWD: "/work"}
	l.SessionID = "01a0696b-8446-7821-a992-9dc7f6a43a25"

	if _, _, err := st.Apply(&core.Op{
		Kind: core.OpSendMessage, Token: sender, To: "busy",
		MsgType: core.MsgQuestion, Body: "blocked on you",
	}, time.Now()); err != nil {
		t.Fatal("setup:", err)
	}
	if !e.hasBlockingMail("busy") {
		t.Fatal("setup: nothing is blocking, so there is nothing to keep asking about")
	}
	e.seen["busy"] = time.Now() // still working

	e.retryWakeDecision("busy")

	e.wakers.mu.Lock()
	timer := e.wakers.deferred["busy"]
	e.wakers.mu.Unlock()
	if timer == nil {
		t.Error("the re-check gave up while a question was still unanswered. One " +
			"deferral only moves the loss one window later: the agent called Dibs " +
			"again, consumed the retry, and the message is stranded exactly as it " +
			"was before")
	}
}

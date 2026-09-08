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

	mail := e.pendingMail("receiver", time.Now())
	if len(mail) == 0 {
		t.Fatal("setup: no pending mail, so this test would pass vacuously")
	}

	surfaces := map[string]string{
		"the wake digest (hook additionalContext)":    hookDigest("receiver", mail, nil, nil),
		"the human notice (hook systemMessage)":       humanNotice("receiver", mail, nil, nil),
		"the waiting line (every tool result)":        e.waiting("receiver", time.Now()),
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

// A persistent agent returning in a NEW session is told it can reattach.
//
// This is the gap that made persistent agents unwakeable, which is the one
// thing they exist for. AgentForHook resolves by session id; an agent that
// registered yesterday holds a session id that died with yesterday's process,
// so a new session of the same agent matches nothing and the hook injects
// nothing. It cannot register its way out of that, because knowing to reattach
// is precisely the thing it would have been told.
//
// Measured on a live board: three messages sat unread for an agent whose human
// was actively using it, with hooks correctly installed and firing, and the
// wake path resolving to nobody on every single turn.
func TestAReturningSessionIsToldItCanReattach(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: "returner", AgentKind: core.KindPersistent,
		Nonce: "kept-nonce", SessionID: "yesterdays-session",
		Agent: &core.AgentInfo{CWD: "/work/api"},
	}); err != nil {
		t.Fatal("setup:", err)
	}
	// Yesterday's process is gone.
	st.Agents["returner"].Status = core.StatusDormant

	res, err := e.HookPoll(ctx, "todays-session", "SessionStart", "/work/api", false, false)
	if err != nil {
		t.Fatal(err)
	}
	out, _ := res["hookSpecificOutput"].(map[string]any)
	ctxText, _ := out["additionalContext"].(string)
	if ctxText == "" {
		t.Fatal("a returning session was told nothing, so a persistent agent stays " +
			"unwakeable and its mail sits unread while its human uses it")
	}
	if !strings.Contains(ctxText, "returner") || !strings.Contains(ctxText, "nonce") {
		t.Errorf("the hint does not name the agent or the credential that recovers "+
			"it, so it is not actionable: %s", ctxText)
	}
	// And it must say NOTHING about that agent's mail: this session has not
	// proved it is that agent, which is the whole reason the directory fallback
	// is refused when a session id was supplied.
	for _, forbidden := range []string{"unread", "message", "from "} {
		if strings.Contains(ctxText, forbidden) {
			t.Errorf("the hint mentions %q. The session has not proved it is that "+
				"agent, and an earlier build answering a stranger by directory handed "+
				"over another agent's mail: %s", forbidden, ctxText)
		}
	}
}

// A live agent is not offered up for reattachment: somebody is being it now,
// and a second session joining would fork the identity rather than recover it.
func TestALiveAgentIsNotOfferedForReattachment(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	st.Agents["busy"] = &core.Agent{
		ID: "busy", Name: "busy", Status: core.StatusActive, Nonce: "n",
		Agent: &core.AgentInfo{CWD: "/work/api"}, Slots: map[string]core.Slot{},
	}
	if got := st.ReattachableIn("/work/api"); len(got) != 0 {
		t.Errorf("a live agent was offered for reattachment: %v", got)
	}
}

// The reattach hint is said ONCE to a session, and never again.
//
// The function that composes it already carried the reason in its own comment:
// "a hook that speaks on every turn is one people disable." It then spoke on
// every turn. `reattachHint` was a pure function of (session, cwd) and state,
// and the Claude Code plugin installs hook_poll on SessionStart,
// UserPromptSubmit, Stop and SubagentStop, so an unregistered session in a
// directory that has idle agents was told to reattach before every prompt it
// answered, for the life of the session.
//
// Reported by an operator whose agent worked out, correctly, that it could not
// turn this off: unregistering makes it fire MORE, because "not registered" is
// the trigger, and the only switch is the plugin's global one, which would take
// Dibs away from every other session on the machine. That is the definition of
// coercive, and rule 4 says this service is advisory.
//
// Once is the whole budget. The hint is a POINTER, and a pointer that did not
// land the first time does not land the tenth; an agent that read it and chose
// not to reattach has decided, and re-asking is nagging a decision. Repeating
// it buys nothing and costs the operator their attention on every turn.
func TestTheReattachHintIsSaidOnce(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: "returner", AgentKind: core.KindPersistent,
		Nonce: "kept-nonce", SessionID: "yesterdays-session",
		Agent: &core.AgentInfo{CWD: "/work/api"},
	}); err != nil {
		t.Fatal("setup:", err)
	}
	st.Agents["returner"].Status = core.StatusDormant

	// The events the shipped plugin really installs, in the order a session
	// meets them.
	events := []string{
		"SessionStart", "UserPromptSubmit", "Stop",
		"UserPromptSubmit", "Stop", "SubagentStop", "UserPromptSubmit",
	}
	var spoke []string
	for _, ev := range events {
		res, err := e.HookPoll(ctx, "todays-session", ev, "/work/api", false, false)
		if err != nil {
			t.Fatal(err)
		}
		out, _ := res["hookSpecificOutput"].(map[string]any)
		if txt, _ := out["additionalContext"].(string); txt != "" {
			spoke = append(spoke, ev)
		}
	}
	if len(spoke) == 0 {
		t.Fatal("the hint was never delivered, so a returning agent is never told " +
			"it can reattach: that is the bug this hint exists to fix")
	}
	if len(spoke) > 1 {
		t.Errorf("the hint was delivered %d times to one session (on %v). It is a "+
			"pointer, not a reminder: repeating it before every prompt is what gets "+
			"the plugin switched off machine-wide, which takes Dibs away from the "+
			"sessions that DO use it", len(spoke), spoke)
	}
	if spoke[0] != "SessionStart" {
		t.Errorf("the one delivery landed on %q rather than SessionStart", spoke[0])
	}

	// A DIFFERENT session in the same directory is still told: the budget is per
	// session, not per directory, or the second agent to arrive hears nothing.
	res, err := e.HookPoll(ctx, "another-session", "SessionStart", "/work/api", false, false)
	if err != nil {
		t.Fatal(err)
	}
	out, _ := res["hookSpecificOutput"].(map[string]any)
	if txt, _ := out["additionalContext"].(string); txt == "" {
		t.Error("a second, different session was told nothing. The hint is spent per " +
			"SESSION; spending it per directory means only the first session of the " +
			"day can ever learn it has an identity waiting")
	}
}

// The hint is prose a PERSON reads over their agent's shoulder, so it has to
// agree with itself in number.
//
// Not pedantry, and not free: the first draft of the plural fix rendered "If
// none of any of them is you" and "If none of it is you", because the singular
// and plural halves were assembled from three independent variables. A list of
// three that "is idle now" is how the original read, and it is the tell that
// nobody looked at the output.
func TestTheReattachHintAgreesInNumber(t *testing.T) {
	mk := func(names ...string) string {
		st := core.NewState("test", core.DefaultLimits())
		for _, n := range names {
			st.Agents[n] = &core.Agent{
				ID: n, Name: n, Status: core.StatusDormant, Nonce: "x",
				Agent: &core.AgentInfo{CWD: "/work/api"}, Slots: map[string]core.Slot{},
			}
		}
		return New(st, &memLedger{}, deadProber{}).reattachHint("s", "/work/api", time.Now())
	}

	one, many := mk("dibs-dev"), mk("a-one", "b-two", "c-three")
	for _, c := range []struct {
		text string
		bad  []string
	}{
		{one, []string{"are idle", "one of those", "none of them", "none of it", "none of any"}},
		{many, []string{"is idle", "If that is you", "If it is not", "none of any"}},
	} {
		if c.text == "" {
			t.Fatal("no hint was composed at all")
		}
		for _, b := range c.bad {
			if strings.Contains(c.text, b) {
				t.Errorf("hint reads %q, which does not agree with how many agents it "+
					"names:\n%s", b, c.text)
			}
		}
	}
}

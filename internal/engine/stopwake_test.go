package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// A wake on Stop has to carry the field that actually continues the turn.
//
// This is the product's one promise: reaching an agent that has stopped, so it
// can read its own mail without its operator noticing first. The delivery was
// sent as `hookSpecificOutput.additionalContext` alone, on the strength of a
// comment saying Claude Code's documentation described that as keeping the
// conversation going. The documentation says the opposite, in a table:
// `decision: "block"` "Prevents Claude from stopping; the conversation
// continues", and additionalContext "does not by itself block the stop".
//
// So every wake this path decided to send landed nowhere, for every Claude
// Code agent on every board. Measured on this machine: mail arrived at
// 21:10:25 while the agent was idle, its Stop hook fired twice in the minutes
// that followed, the daemon returned the digest both times, and the agent
// found out an hour later because its operator told it.
//
// Nothing in the daemon could see it. The digest was computed, the freshness
// was spent, the systemMessage reached the human, and the one key that makes a
// harness act was missing: a wake that reports success while doing nothing.
func TestAWakeOnStopCarriesTheFieldThatContinuesTheTurn(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	const sid = "11111111-2222-4333-8444-555555555555"
	sleeper, err := e.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: "sleeper", Nonce: "n-sleeper", SessionID: sid,
	})
	if err != nil {
		t.Fatalf("setup: register sleeper: %v", err)
	}
	sender, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "sender", Nonce: "n-sender"})
	if err != nil {
		t.Fatalf("setup: register sender: %v", err)
	}
	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpSendMessage, Token: sender["token"].(string), To: "sleeper",
		MsgType: "notify", Body: "a peer needs you to know something",
	}); err != nil {
		t.Fatalf("setup: send: %v", err)
	}
	_ = sleeper

	got, err := e.HookPoll(ctx, sid, "Stop", "/anywhere", false, false)
	if err != nil {
		t.Fatalf("hook poll: %v", err)
	}
	// The digest itself: what the agent reads.
	hso, _ := got["hookSpecificOutput"].(map[string]any)
	if hso == nil {
		t.Fatalf("no hookSpecificOutput at all; got %v", got)
	}
	ctxText, _ := hso["additionalContext"].(string)
	if !strings.Contains(ctxText, "unread") {
		t.Fatalf("the digest does not mention the unread mail: %q", ctxText)
	}
	// And the field without which the turn simply ends.
	if got["decision"] != "block" {
		t.Errorf(`decision = %v, want "block". additionalContext alone does not `+
			`continue a turn: the mail is computed, the freshness is spent, and the `+
			`agent never reads a word of it`, got["decision"])
	}
	reason, _ := got["reason"].(string)
	if !strings.Contains(reason, "unread") {
		t.Errorf("reason = %q: it is what Claude is shown when the stop is blocked, "+
			"so it has to carry the news", reason)
	}
}

// And a strict caller (Codex) gets the continuation in the shape ITS Stop
// accepts.
//
// This asserted the opposite: that a strict Stop must NOT carry decision and
// reason and must carry hookSpecificOutput. Codex's StopCommandOutputWire has
// always been the reverse (decision and reason, no hookSpecificOutput, deny
// unknown fields), so the test pinned the one reply Codex refuses at Stop, and
// no Codex agent was ever continued for its mail. Read at the installed tag,
// rust-v0.159.2, and at every tag back to 0.153.
//
// This asserts the FILTER, which is the single place that knows what a caller
// accepts. One rule, one place.
func TestAStrictStopContinuesTheTurnTheWayCodexAccepts(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	const sid = "99999999-8888-4777-8666-555555555555"
	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: "codexish", Nonce: "n-codexish", SessionID: sid,
	}); err != nil {
		t.Fatalf("setup: register: %v", err)
	}
	sender, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "peer", Nonce: "n-peer"})
	if err != nil {
		t.Fatalf("setup: register peer: %v", err)
	}
	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpSendMessage, Token: sender["token"].(string), To: "codexish",
		MsgType: "notify", Body: "something to read",
	}); err != nil {
		t.Fatalf("setup: send: %v", err)
	}

	got, err := e.HookPoll(ctx, sid, "Stop", "/anywhere", false, true) // strict
	if err != nil {
		t.Fatalf("hook poll: %v", err)
	}
	if _, present := got["hookSpecificOutput"]; present {
		t.Error("a strict Stop carries hookSpecificOutput, which Codex's Stop schema does " +
			"not have: the whole parse fails and the agent is told nothing")
	}
	if got["decision"] != "block" {
		t.Errorf(`decision = %v, want "block": it is the only way Codex continues a turn`, got["decision"])
	}
	if reason, _ := got["reason"].(string); !strings.Contains(reason, "unread") {
		t.Errorf("reason = %q: it becomes the model's next prompt, so it has to carry the news", reason)
	}
}

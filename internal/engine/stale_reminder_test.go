package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// A live session that has stopped coordinating is told so, without a turn
// being extended for it.
//
// An agent registers, declares, and works for hours without calling Dibs
// again; the board reports it dormant while it is busy, and peers writing to
// it are told so. Issue #53. The reminder rides on a digest delivered for
// another reason and on the ambient line to the person; alone on a Stop it
// extends nothing, and it repeats no more often than its own interval.
func TestAStaleSessionIsRemindedWithoutExtendingATurn(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	e.SetStaleReminder(30 * time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	reg := func(name string) string {
		res, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: name})
		if err != nil {
			t.Fatalf("setup: register %s: %v", name, err)
		}
		tok, _ := res["token"].(string)
		if _, err := e.Do(ctx, &core.Op{Kind: core.OpAckBoard, Token: tok}); err != nil {
			t.Fatalf("setup: ack %s: %v", name, err)
		}
		return tok
	}
	quiet, peer := reg("quiet"), reg("peer")
	const sid = "quiet-session"
	if _, err := e.BindSession(ctx, quiet, sid); err != nil {
		t.Fatalf("setup: bind: %v", err)
	}
	// Two hours of silence, as the board would record it.
	age := func() {
		_, _ = e.query(ctx, func() core.Result {
			e.state.Agents["quiet"].LastCoordination = time.Now().Add(-2 * time.Hour)
			return core.Result{}
		})
	}
	age()

	// Alone, on a Stop: the person is told, the model is not woken.
	res, err := e.HookPoll(ctx, sid, "Stop", "", false, false)
	if err != nil {
		t.Fatalf("hook_poll: %v", err)
	}
	if _, extended := res["hookSpecificOutput"]; extended {
		t.Fatalf("a stale reminder with no other news extended a turn: %v", res)
	}
	human, _ := res["systemMessage"].(string)
	if !strings.Contains(human, "check_in") || !strings.Contains(human, "2h") {
		t.Errorf("systemMessage = %q, want the ambient line to name the silence and the corrective call", human)
	}

	// With news: it rides on the digest that is being delivered anyway.
	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpSendMessage, Token: peer, To: "quiet", MsgType: core.MsgNotify, Body: "fyi",
	}); err != nil {
		t.Fatalf("setup: send: %v", err)
	}
	age() // the send was the peer's op, not quiet's, but make the ageing explicit
	res, err = e.HookPoll(ctx, sid, "Stop", "", false, false)
	if err != nil {
		t.Fatalf("hook_poll: %v", err)
	}
	hso, _ := res["hookSpecificOutput"].(map[string]any)
	digest, _ := hso["additionalContext"].(string)
	if !strings.Contains(digest, "have not coordinated") || !strings.Contains(digest, "check_in") {
		t.Errorf("digest = %q, want the reminder riding on the delivered mail", digest)
	}

	// Throttled: the same silence is not restated on the very next Stop.
	res, err = e.HookPoll(ctx, sid, "Stop", "", false, false)
	if err != nil {
		t.Fatalf("hook_poll: %v", err)
	}
	if human, _ := res["systemMessage"].(string); strings.Contains(human, "check_in") {
		t.Errorf("the reminder repeated within its own interval: %q", human)
	}

	// And it clears the moment the agent coordinates.
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpAckBoard, Token: quiet}); err != nil {
		t.Fatalf("check_in: %v", err)
	}
	_, _ = e.query(ctx, func() core.Result {
		if r := e.staleReminder(e.state.Agents["quiet"], time.Now().Add(29*time.Minute)); r != "" {
			t.Errorf("reminder after a check_in: %q", r)
		}
		return core.Result{}
	})
}

// Off means off: an engine nobody configured reminds nobody.
func TestTheStaleReminderIsOffUntilConfigured(t *testing.T) {
	e := &Engine{}
	l := &core.Agent{ID: "a", LastCoordination: time.Now().Add(-48 * time.Hour)}
	if r := e.staleReminder(l, time.Now()); r != "" {
		t.Errorf("reminder with no interval set: %q", r)
	}
}

package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// Mail has to reach an agent whose harness cannot push anything at all.
//
// Push delivery is a stack of ifs: the harness must have lifecycle hooks, the
// plugin must be installed, it must have been loaded before the session began,
// and the agent must have registered with the session id the hook will quote.
// Two harnesses satisfy the first. Measured on a live board: an agent that had
// registered without a session id sat on unread mail for hours while
// `dibs doctor` reported hooks resolving perfectly, because they were, for
// everybody else.
//
// The result of a call the agent itself made is the only channel that always
// exists, and it cannot be misrouted: it goes back down the connection the
// caller authenticated on. So this test goes through the ENGINE the way an
// agent does, and asserts an ordinary unrelated call carries the news.
func TestAnOrdinaryCallTellsAnAgentItHasMail(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	tok := func(name string) string {
		t.Helper()
		res, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: name})
		if err != nil {
			t.Fatalf("setup: register %s: %v", name, err)
		}
		v, _ := res["token"].(string)
		if v == "" {
			t.Fatalf("setup: register %s returned no token", name)
		}
		if _, err := e.Do(ctx, &core.Op{Kind: core.OpAckBoard, Token: v}); err != nil {
			t.Fatalf("setup: check_in %s: %v", name, err)
		}
		return v
	}
	alpha, beta := tok("alpha"), tok("beta")

	// Nothing waiting: the key must be absent, or every result grows a line
	// saying nothing and agents learn to skip it.
	quiet, err := e.Do(ctx, &core.Op{Kind: core.OpHeartbeat, Token: beta})
	if err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	if quiet["waiting"] != nil {
		t.Errorf("waiting = %v with an empty inbox: it must be absent when there is nothing",
			quiet["waiting"])
	}

	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpSendMessage, Token: alpha, To: "beta",
		MsgType: core.MsgQuestion, Body: "are you working on the ledger?",
	}); err != nil {
		t.Fatalf("setup: send: %v", err)
	}

	// A heartbeat has nothing to do with mail. That is the point: the agent
	// learns about the question without having asked about mail at all.
	res, err := e.Do(ctx, &core.Op{Kind: core.OpHeartbeat, Token: beta})
	if err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	w, _ := res["waiting"].(string)
	if w == "" {
		t.Fatal("an ordinary call carried no word of the waiting question: an agent on a " +
			"harness with no hooks has no way to find out")
	}
	if !strings.Contains(w, "inbox") {
		t.Errorf("waiting = %q: it must name the call that reads them", w)
	}
	// Counts, never content. The nudge rides every call an agent makes, and the
	// body is private to the mailbox: inbox is authenticated, this is ambient.
	if strings.Contains(w, "are you working on the ledger") {
		t.Errorf("waiting = %q: it quotes the message body", w)
	}

	// check_in is exempt, because it has just returned the inbox itself.
	ci, err := e.Do(ctx, &core.Op{Kind: core.OpAckBoard, Token: beta})
	if err != nil {
		t.Fatalf("check_in: %v", err)
	}
	if ci["waiting"] != nil {
		t.Errorf("check_in carried waiting=%v: it already returned the mail", ci["waiting"])
	}
}

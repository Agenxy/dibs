// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// A Claude Code session that asks the person a question waits, mid-turn, for
// as long as the person takes, and queues every peer message that arrives
// meanwhile. agenxy-supply waited six hours, had eighteen notices queued, and
// took all eighteen in one burst when the person answered: several repeated
// the same stale digest, each ran its own UserPromptSubmit hook, and the
// session compacted a minute later. One queued notice is the wake; the hook
// that runs when it is taken up presents everything outstanding by then.
func TestASessionWaitingOnThePersonGetsOneNoticeNotOnePerItem(t *testing.T) {
	f := newDaemonEconomyFixture(t)
	f.hook(t, "UserPromptSubmit", false) // the turn in which the agent asks the person
	send := func(body string) uint64 {
		t.Helper()
		r := f.do(t, &core.Op{Kind: core.OpSendMessage, Token: f.sender, To: "worker", MsgType: core.MsgQuestion, Body: body})
		return r["msg_serial"].(uint64)
	}
	quiet := func(why string) {
		t.Helper()
		select {
		case text := <-f.wire:
			t.Fatalf("%s: %q", why, text)
		case <-time.After(1100 * time.Millisecond):
		}
	}

	send("while-asking-first")
	if text := f.receive(t, time.Second); !strings.Contains(text, "while-asking-first") {
		t.Fatalf("the first item must still wake the session: %q", text)
	}
	// Well past the plain busy ceiling, which must not revive one write per item.
	f.ageBusy(t, 2*time.Hour)
	second, third := send("while-asking-second"), send("while-asking-third")
	quiet("a notice was queued behind one the session has not taken up")

	// The person answers; the harness takes the queued notice up.
	got := fmtResult(f.hook(t, "UserPromptSubmit", false))
	for _, serial := range []uint64{second, third} {
		if !strings.Contains(got, "#"+strconv.FormatUint(serial, 10)+" question") {
			t.Fatalf("the hook that runs on take-up must present #%d: %s", serial, got)
		}
	}
	send("after-answer")
	if text := f.receive(t, time.Second); !strings.Contains(text, "after-answer") {
		t.Fatalf("once the queued notice was taken up, new mail must be written: %q", text)
	}
}

// A receiver can hold a peer message rather than queue it (bypassPermissions
// mode does), and then no turn ever takes it up. The end of the turn must
// restore a write per new item, or one held notice would silence the session.
func TestAQueuedNoticeStopsHoldingBackWritesWhenTheTurnEnds(t *testing.T) {
	f := newDaemonEconomyFixture(t)
	f.hook(t, "UserPromptSubmit", false)
	f.do(t, &core.Op{Kind: core.OpSendMessage, Token: f.sender, To: "worker", MsgType: core.MsgQuestion, Body: "held-first"})
	if text := f.receive(t, time.Second); !strings.Contains(text, "held-first") {
		t.Fatalf("setup: first write: %q", text)
	}
	// The first Stop delivers the held item by blocking, so the turn goes on;
	// the turn that ends with nothing left to deliver is the idle evidence.
	for range 3 {
		if f.hook(t, "Stop", false)["decision"] != "block" {
			break
		}
	}
	f.do(t, &core.Op{Kind: core.OpSendMessage, Token: f.sender, To: "worker", MsgType: core.MsgQuestion, Body: "after-stop"})
	if text := f.receive(t, time.Second); !strings.Contains(text, "after-stop") {
		t.Fatalf("an idle session must get a write per new item: %q", text)
	}
}

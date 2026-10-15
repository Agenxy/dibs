// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package ledger

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/mailhistory"
)

// serveHistory starts the mail-history view the way production does, through
// the serving listener's first Accept, and waits for its boot fold to finish.
func serveHistory(t *testing.T, l *Ledger) mailhistory.Measurement {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	srv.Listener = l.MailHistory().ServingListener(ctx, srv.Listener)
	srv.Start()
	t.Cleanup(srv.Close)
	res, err := srv.Client().Get(srv.URL)
	if err != nil {
		t.Fatal("setup: serving", err)
	}
	_ = res.Body.Close()
	return awaitBootHistory(t, l.MailHistory())
}

// A ledger that continues from a snapshot holds conversations that began
// before it. The first line after the snapshot that touches one of them can
// come after a newer conversation's first line, which a ledger folded from
// genesis never does, because there a conversation first appears on the line
// that creates it. The history view assumed that order and refused to build:
// on the production board mail_history answered E_HISTORY_UNAVAILABLE for
// every caller from the first compaction on, and nothing was logged.
func TestHistoryBuildsFromASnapshotWhenAnOlderConversationIsTouchedLater(t *testing.T) {
	dir := t.TempDir()
	lp := filepath.Join(dir, "ledger.jsonl")
	box, err := LoadOrCreateKey(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	old := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	recent := old.Add(48 * time.Hour)
	l, err := Open(lp, "test", box)
	if err != nil {
		t.Fatal(err)
	}
	st := core.NewState("test", core.DefaultLimits())
	apply(t, st, l, &core.Op{Kind: core.OpRegister, Name: "sender", NewToken: "sender-token", Nonce: "sn", AgentKind: core.KindPersistent}, old)
	apply(t, st, l, &core.Op{Kind: core.OpRegister, Name: "worker", NewToken: "worker-token", Nonce: "wn", AgentKind: core.KindPersistent}, old)
	apply(t, st, l, &core.Op{Kind: core.OpSendMessage, Token: "sender-token", To: "worker", MsgType: core.MsgQuestion, Body: "older"}, old)
	asked := st.Serial
	// Inside the kept window: a NEW conversation first, then the older one.
	apply(t, st, l, &core.Op{Kind: core.OpSendMessage, Token: "sender-token", To: "worker", MsgType: core.MsgNotify, Body: "newer"}, recent)
	apply(t, st, l, &core.Op{Kind: core.OpRespond, Token: "worker-token", MsgSerial: asked, Disposition: "answer", Body: "late answer"}, recent)
	_ = l.Close()

	reopen := func() *Ledger {
		t.Helper()
		led, err := Open(lp, "test", box)
		if err != nil {
			t.Fatal(err)
		}
		led.KeepHistory(24 * time.Hour)
		led.clock = func() time.Time { return recent.Add(time.Hour) }
		if _, err := led.Replay(core.NewState("test", core.DefaultLimits())); err != nil {
			t.Fatal("setup: replay", err)
		}
		return led
	}
	compacting := reopen()
	if !compacting.anchored {
		t.Fatal("setup: the first start did not compact")
	}
	if m := serveHistory(t, compacting); m.Conversations != 2 {
		t.Fatalf("history after compacting: %+v", m)
	}
	_ = compacting.Close()
	kept, err := os.ReadFile(lp)
	if err != nil {
		t.Fatal(err)
	}
	// And every start after it, from the snapshot alone, which keeps every
	// line still inside the window: it used to fold one more on each start.
	later := reopen()
	defer func() { _ = later.Close() }()
	if again, _ := os.ReadFile(lp); string(again) != string(kept) {
		t.Fatalf("a start with nothing older than the window compacted again: %d bytes, was %d", len(again), len(kept))
	}
	if m := serveHistory(t, later); m.Conversations != 2 {
		t.Fatalf("history from the snapshot: %+v", m)
	}
}

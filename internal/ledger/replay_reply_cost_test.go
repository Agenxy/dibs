// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package ledger

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// Both production replay doors discard Apply's reply. Growing fleet size
// must not multiply reply-board allocation at every historical checkpoint.
// Count allocations, not elapsed time: the old all-board projection has
// quadratic growth even when the runner happens to finish within its deadline.
func TestReplayReplyAllocationScalesWithRecords(t *testing.T) {
	for _, history := range []bool{false, true} {
		label := "ledger"
		if history {
			label = "serving-history"
		}
		t.Run(label, func(t *testing.T) {
			small := replayReplyAllocations(t, 24, history)
			large := replayReplyAllocations(t, 240, history)
			t.Logf("REPLAY_REPLY_ALLOCS small=%.0f large=%.0f ratio=%.3f", small, large, large/small)
			// Ten times the records allows twice the linear growth. This leaves
			// room for codec/map boundaries without admitting a board per op.
			if large > 20*small {
				t.Fatalf("replay allocates discarded reply boards: 10x records cost %.2fx allocations", large/small)
			}
		})
	}
}

func replayReplyAllocations(t *testing.T, size int, history bool) float64 {
	t.Helper()
	l, _ := newLedger(t)
	now := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	l.clock = func() time.Time { return now }
	limits := core.DefaultLimits()
	limits.MaxAgents, limits.MaxPersistentAgents = 512, 512
	live := core.NewState("test", limits)
	for n := 0; n < size; n++ {
		name := fmt.Sprintf("allocation-source-%03d", n)
		token := "token-" + name
		apply(t, live, l, &core.Op{Kind: core.OpRegister, Name: name, NewToken: token, MintedNonce: "nonce-" + name, AgentKind: core.KindPersistent, SessionID: name, V7Semantics: true}, now)
		apply(t, live, l, &core.Op{Kind: core.OpAckBoard, Token: token}, now.Add(time.Second))
		res := apply(t, live, l, &core.Op{Kind: core.OpRegister, Name: name, NewToken: "return-" + name, SessionID: name, V7Semantics: true}, now.Add(2*time.Second))
		if res["reattached"] != true || res["via"] != "session_id" {
			t.Fatal("setup: session register did not reattach its own minted-nonce identity")
		}
		apply(t, live, l, &core.Op{Kind: core.OpResume, Nonce: "nonce-" + name, ResumeID: "resume-" + name, NewToken: "resume-" + name, SessionID: name, V7Semantics: true}, now.Add(3*time.Second))
		if live.Agents[name].Token != "resume-"+name {
			t.Fatal("setup: explicit resume did not rotate the actual credential")
		}
	}
	if live.Serial != uint64(4*size) || len(live.Agents) != size {
		t.Fatal("setup: registration/checkpoint fixture did not apply")
	}
	measure := func() {
		state := core.NewState("test", limits)
		if count, err := l.Replay(state); err != nil || count != 4*size || state.Serial != live.Serial || len(state.Agents) != size {
			t.Fatalf("setup: production replay: count=%d serial=%d agents=%d err=%v", count, state.Serial, len(state.Agents), err)
		}
	}
	var before, after runtime.MemStats
	if !history {
		runtime.ReadMemStats(&before)
		measure()
		runtime.ReadMemStats(&after)
		return float64(after.Mallocs - before.Mallocs)
	}
	// Replay itself is setup here. Only the real serving-triggered background
	// fold belongs to this measurement, so fixing one door cannot hide the other.
	measure()
	if m := l.MailHistory().Measurement(); m.Started || m.Ready {
		t.Fatal("setup: history must wait for the real serving door", m)
	}
	runtime.ReadMemStats(&before)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	srv.Listener = l.MailHistory().ServingListener(ctx, srv.Listener)
	srv.Start()
	defer srv.Close()
	response, err := srv.Client().Get(srv.URL)
	if err != nil {
		t.Fatal("setup: actual serving Accept", err)
	}
	_ = response.Body.Close()
	m := awaitBootHistory(t, l.MailHistory())
	if m.BuiltSerial != live.Serial || m.Records != uint64(4*size) {
		t.Fatal("history fold did not reach its actual boot boundary", m)
	}
	runtime.ReadMemStats(&after)
	return float64(after.Mallocs - before.Mallocs)
}

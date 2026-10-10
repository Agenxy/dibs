// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/ledger"
)

// Build real historical delivery receipts and accepted work in an encrypted
// ledger. Every measurement enters through MCP after production replay/boot;
// no page, notification cache or new feature flag is installed by the fixture.
func boundedMailServer(t *testing.T) (*httptest.Server, []uint64, *engine.Engine) {
	t.Helper()
	dir := t.TempDir()
	box, err := ledger.LoadOrCreateKey(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	led, err := ledger.Open(filepath.Join(dir, "ledger.jsonl"), "bounded", box)
	if err != nil {
		t.Fatal(err)
	}
	st := core.NewState("bounded", core.DefaultLimits())
	now := time.Now().Add(-time.Hour)
	apply := func(op *core.Op) core.Result {
		t.Helper()
		res, _, e := st.Apply(op, now)
		if e != nil {
			t.Fatalf("setup %s: %v", op.Kind, e)
		}
		if e = led.Append(st.Serial, now, op); e != nil {
			t.Fatal(e)
		}
		return res
	}
	for _, name := range []string{"sender", "reader"} {
		apply(&core.Op{Kind: core.OpRegister, Name: name, NewToken: name + "-token", Nonce: name + "-nonce", SessionID: name + "-session", AgentKind: core.KindPersistent})
		apply(&core.Op{Kind: core.OpAckBoard, Token: name + "-token"})
	}
	var fyis []uint64
	for i := range 140 {
		res := apply(&core.Op{Kind: core.OpSendMessage, Token: "sender-token", To: "reader", MsgType: core.MsgNotify, Body: fmt.Sprintf("FYI %d %s", i, strings.Repeat("substantial retained message ", 70))})
		fyis = append(fyis, res["msg_serial"].(uint64))
	}
	apply(&core.Op{Kind: core.OpMarkDelivered, MsgSerials: fyis})
	var owed []uint64
	for i := range 2 {
		res := apply(&core.Op{Kind: core.OpSendMessage, Token: "sender-token", To: "reader", MsgType: core.MsgRequest, Body: fmt.Sprintf("owed work %d", i), DeadlineSec: 86400})
		id := res["msg_serial"].(uint64)
		owed = append(owed, id)
		apply(&core.Op{Kind: core.OpRespond, Token: "reader-token", MsgSerial: id, Disposition: "approve"})
	}
	replay := core.NewState("bounded", core.DefaultLimits())
	var events []core.Event
	led.OnEvents = func(x []core.Event) { events = append(events, x...) }
	if _, err = led.Replay(replay); err != nil {
		t.Fatal(err)
	}
	eng := engine.New(replay, led, nil, events)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { eng.Run(ctx); close(done) }()
	srv := httptest.NewServer(New(eng))
	t.Cleanup(func() {
		srv.Close()
		cancel()
		<-done
		if err := led.Close(); err != nil {
			t.Error(err)
		}
	})
	return srv, owed, eng
}

func TestBoundedMailboxPrioritizesOwedWorkAndPagesEveryFYI(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		for _, method := range []string{"inbox", "check_in"} {
			t.Run(version+"/"+method, func(t *testing.T) {
				srv, owed, eng := boundedMailServer(t)
				first := waitingCall(t, srv, version, method, map[string]any{"token": "reader-token"})
				mail, ok := first["inbox"].([]any)
				if !ok || len(mail) < 2 {
					t.Fatalf("missing owed work page: %v", first)
				}
				for i, id := range owed {
					if mail[i].(map[string]any)["serial"] != float64(id) {
						t.Fatalf("owed request %d not first: %v", id, mail[:2])
					}
				}
				seen := map[float64]bool{}
				page := first
				for step := 0; step < 160; step++ {
					raw, err := json.Marshal(page)
					if err != nil {
						t.Fatal(err)
					}
					if method == "inbox" && len(raw) > 16384 {
						t.Fatalf("inbox exceeds16KiB: %d", len(raw))
					}
					items := page["inbox"].([]any)
					if len(items) > 8 {
						t.Fatalf("unbounded page length: %d", len(items))
					}
					for _, v := range items {
						id := v.(map[string]any)["serial"].(float64)
						if seen[id] {
							t.Fatalf("pagination repeated %v", id)
						}
						seen[id] = true
					}
					cursor, _ := page["next_cursor"].(string)
					if cursor == "" {
						break
					}
					if err := eng.SetRateTokens(context.Background(), "reader", 60); err != nil {
						t.Fatal(err)
					}
					page = waitingCall(t, srv, version, "inbox", map[string]any{"token": "reader-token", "cursor": cursor})
				}
				if len(seen) != 142 {
					t.Fatalf("pagination lost retained mail: got%d want142", len(seen))
				}
			})
		}
	}
}

func TestFYIFullPresentationClearsWithoutAckAndKeepsOwedWork(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		t.Run(version, func(t *testing.T) {
			srv, owed, eng := boundedMailServer(t)
			page := waitingCall(t, srv, version, "inbox", map[string]any{"token": "reader-token"})
			presented := 0
			for step := 0; step < 160; step++ {
				for _, v := range page["inbox"].([]any) {
					item := v.(map[string]any)
					if item["type"] != "notify" {
						continue
					}
					id := uint64(item["serial"].(float64))
					m := mailboxReceipt(t, eng, id)
					if item["body"] != m.Body || item["body_truncated"] == true || !m.Consumed || m.AckedAt == 0 {
						t.Fatalf("complete bounded FYI presentation was not consumed: %v %+v", item, m)
					}
					presented++
				}
				cursor, _ := page["next_cursor"].(string)
				if cursor == "" {
					break
				}
				if err := eng.SetRateTokens(context.Background(), "reader", 60); err != nil {
					t.Fatal(err)
				}
				page = waitingCall(t, srv, version, "inbox", map[string]any{"token": "reader-token", "cursor": cursor})
			}
			if presented != 140 {
				t.Fatalf("presented %d FYIs, want140", presented)
			}
			for _, id := range owed {
				m := mailboxReceipt(t, eng, id)
				if !m.Owed(time.Now()) {
					t.Fatal("presentation consumed owed request")
				}
			}
			for _, event := range []string{"UserPromptSubmit", "Stop", "SessionStart", "UserPromptSubmit"} {
				got := waitingCall(t, srv, version, "hook_poll", map[string]any{"session_id": "reader-session", "event": event})
				if strings.Contains(fmt.Sprint(got), "FYI") || got["decision"] == "block" {
					t.Fatalf("presented FYIs reminded or bought a turn: %v", got)
				}
			}
			// Read the newest FYI: the existing 128-terminal retention cap may
			// evict the oldest twelve after this 140-item traversal.
			full := waitingCall(t, srv, version, "read_mail", map[string]any{"token": "reader-token", "msg_serial": 144})
			if !strings.Contains(fmt.Sprint(full), "substantial retained message") {
				t.Fatal("consumed FYI lost recent read_mail retention")
			}
		})
	}
}

func TestAnnouncedFYIHasOneFurtherReminderThenStops(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		for _, route := range []string{"Stop", "oversized read_mail"} {
			t.Run(version+"/"+route, func(t *testing.T) {
				srv, _ := newServer(t)
				reader := toolCall(t, srv, "register", map[string]any{"name": "reader", "session_id": "reader-session"})
				sender := toolCall(t, srv, "register", map[string]any{"name": "sender"})
				r := waitingCall(t, srv, version, "send", map[string]any{"token": sender["token"], "to": "reader", "type": "notify", "body": strings.Repeat("oversized FYI ", 2000)})
				id := uint64(r["msg_serial"].(float64))
				eng := srv.Config.Handler.(*Server).eng
				var first map[string]any
				if route == "Stop" {
					first = waitingCall(t, srv, version, "hook_poll", map[string]any{"session_id": "reader-session", "event": "Stop"})
					if !strings.Contains(fmt.Sprint(first), "trimmed; read_mail") {
						t.Fatalf("setup: hook did not shorten the FYI: %v", first)
					}
				} else {
					first = waitingCall(t, srv, version, "read_mail", map[string]any{"token": reader["token"], "msg_serial": id})
					if !strings.Contains(fmt.Sprint(first), strings.Repeat("oversized FYI ", 2000)) {
						t.Fatal("setup: oversized read lost body")
					}
				}
				if mailboxReceipt(t, eng, id).Consumed {
					t.Fatalf("shortened hook or oversized read falsely consumed FYI: %s", route)
				}
				reminder := waitingCall(t, srv, version, "hook_poll", map[string]any{"session_id": "reader-session", "event": "UserPromptSubmit"})
				if !strings.Contains(fmt.Sprint(reminder), fmt.Sprintf("#%d", id)) || reminder["decision"] == "block" || !mailboxReceipt(t, eng, id).Consumed {
					t.Fatalf("one passive reminder not recorded/consumed: %v", reminder)
				}
				for _, event := range []string{"UserPromptSubmit", "Stop", "SessionStart"} {
					got := waitingCall(t, srv, version, "hook_poll", map[string]any{"session_id": "reader-session", "event": event})
					if strings.Contains(fmt.Sprint(got), fmt.Sprintf("#%d", id)) || got["decision"] == "block" {
						t.Fatalf("FYI repeated after its reminder: %v", got)
					}
				}
				waitingCall(t, srv, version, "ack", map[string]any{"token": reader["token"], "msg_serial": id})
			})
		}
	}
}

func TestNaturalHookRecoversBacklogWithoutHumanNoticeOrRepeatedStop(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		t.Run(version, func(t *testing.T) {
			srv, _, _ := boundedMailServer(t)
			for _, event := range []string{"Stop", "UserPromptSubmit", "SessionStart"} {
				got := waitingCall(t, srv, version, "hook_poll", map[string]any{"session_id": "reader-session", "event": event})
				if _, human := got["systemMessage"]; human {
					t.Fatalf("agent mail handed to human at%s: %v", event, got)
				}
				if event == "Stop" {
					if got["decision"] == "block" {
						t.Fatalf("spent FYIs bought another turn: %v", got)
					}
					continue
				}
				h, ok := got["hookSpecificOutput"].(map[string]any)
				if !ok {
					t.Fatalf("%s missed passive recovery: %v", event, got)
				}
				digest, _ := h["additionalContext"].(string)
				if !strings.Contains(digest, "announced FYIs awaiting presentation") || strings.Contains(digest, "140 unread") {
					t.Fatalf("dishonest passive counts: %q", digest)
				}
				if len(digest) > 8192 {
					t.Fatalf("passive digest unbounded: %d", len(digest))
				}
				if got["decision"] == "block" {
					t.Fatal("natural activation bought another turn")
				}
			}
		})
	}
}

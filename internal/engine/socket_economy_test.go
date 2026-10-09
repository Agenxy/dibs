// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

type daemonEconomyFixture struct {
	e          *Engine
	ctx        context.Context
	sock       net.Listener
	sid, token string
	sender     string
	wire       <-chan string
}

func newDaemonEconomyFixture(t *testing.T) *daemonEconomyFixture {
	t.Helper()
	sock, sid := listeningSession(t)
	f := &daemonEconomyFixture{sock: sock, sid: sid}
	f.e = New(core.NewState("economy", core.DefaultLimits()), &memLedger{}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	f.ctx = ctx
	joined := make(chan struct{})
	go func() { f.e.Run(ctx); close(joined) }()
	stopWakeTimersOnCleanup(t, f.e)
	t.Cleanup(func() { cancel(); <-joined })
	for _, name := range []string{"sender", "worker"} {
		session := ""
		if name == "worker" {
			session = sid
		}
		r := f.do(t, &core.Op{
			Kind: core.OpRegister, Name: name, SessionID: session,
			Agent: &core.AgentInfo{Harness: "Claude Code", CWD: "/w", Host: "economy"},
		})
		if name == "worker" {
			f.token = r["token"].(string)
		} else {
			f.sender = r["token"].(string)
		}
	}
	if own, _ := f.e.SelfWaking("worker"); own {
		t.Fatal("setup: daemon fallback unexpectedly has a claiming bridge")
	}
	f.hook(t, "Stop", true)
	wire := make(chan string, 8)
	f.wire = wire
	go func() {
		for {
			text := readAll(t, sock)
			if text == "" {
				return
			}
			wire <- text
		}
	}()
	return f
}

func (f *daemonEconomyFixture) do(t *testing.T, op *core.Op) core.Result {
	t.Helper()
	r, err := f.e.Do(f.ctx, op)
	if err != nil {
		t.Fatal("setup: real engine operation:", err)
	}
	return r
}

func (f *daemonEconomyFixture) hook(t *testing.T, event string, active bool) core.Result {
	t.Helper()
	r, err := f.e.HookPoll(f.ctx, f.sid, event, "", active, false)
	if err != nil {
		t.Fatal("setup: real lifecycle hook:", err)
	}
	return r
}

func (f *daemonEconomyFixture) receive(t *testing.T, within time.Duration) string {
	t.Helper()
	select {
	case text := <-f.wire:
		return text
	case <-time.After(within):
		t.Fatal("no actual daemon socket write")
		return ""
	}
}

// Advance only the already-observed busy timestamp on the writer loop. The
// fixture must enter busy through production auth/hooks, never a flag setter.
func (f *daemonEconomyFixture) ageBusy(t *testing.T, age time.Duration) {
	t.Helper()
	_, err := f.e.query(f.ctx, func() core.Result {
		key := socketSessionKey(f.e.state.Agents["worker"])
		turn := f.e.socketTurns[key]
		if turn.state != "busy" || turn.at.IsZero() {
			t.Error("setup: no production busy observation to advance")
			return nil
		}
		turn.at = time.Now().Add(-age)
		f.e.socketTurns[key] = turn
		f.e.pruneSocketState()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSocketEconomyAuthenticatedBusyWithoutStopStillDelivers(t *testing.T) {
	f := newDaemonEconomyFixture(t)
	f.do(t, &core.Op{Kind: core.OpAckBoard, Token: f.token})
	for _, body := range []string{"missing-stop-first", "missing-stop-second"} {
		f.do(t, &core.Op{Kind: core.OpSendMessage, Token: f.sender, To: "worker", MsgType: core.MsgQuestion, Body: body})
		text := f.receive(t, time.Second)
		if !strings.Contains(text, body) || (body == "missing-stop-second" && strings.Contains(text, "missing-stop-first")) {
			t.Fatalf("busy delivery lost new mail or repeated an old item: %q", text)
		}
	}
	select {
	case text := <-f.wire:
		t.Fatalf("no new item bought another write: %q", text)
	case <-time.After(1100 * time.Millisecond):
	}
}

func TestSocketBurstCarriesAllNewItemsAndNeverRepeats(t *testing.T) {
	f := newDaemonEconomyFixture(t)
	for _, body := range []string{"burst-first", "burst-second"} {
		f.do(t, &core.Op{Kind: core.OpSendMessage, Token: f.sender, To: "worker", MsgType: core.MsgQuestion, Body: body})
	}
	if text := f.receive(t, time.Second); !strings.Contains(text, "burst-first") || !strings.Contains(text, "burst-second") {
		t.Fatalf("burst did not carry every original item: %q", text)
	}
	select {
	case text := <-f.wire:
		t.Fatalf("burst wrote twice: %q", text)
	case <-time.After(1100 * time.Millisecond):
	}
	f.do(t, &core.Op{Kind: core.OpSendMessage, Token: f.sender, To: "worker", MsgType: core.MsgQuestion, Body: "separate-third"})
	if text := f.receive(t, time.Second); !strings.Contains(text, "separate-third") || strings.Contains(text, "burst-first") || strings.Contains(text, "burst-second") {
		t.Fatalf("later mail missing or prior cohort repeated: %q", text)
	}
}

func TestSocketFreshAnnouncementAndOutcomeAreNotTurnSuppressed(t *testing.T) {
	for _, kind := range []string{"announcement", "outcome"} {
		t.Run(kind, func(t *testing.T) {
			f := newDaemonEconomyFixture(t)
			if kind == "announcement" {
				f.do(t, &core.Op{Kind: core.OpAckBoard, Token: f.sender})
				f.do(t, &core.Op{Kind: core.OpAckBoard, Token: f.token})
				f.do(t, &core.Op{Kind: core.OpSpaceOpen, Token: f.sender, Space: "fresh-proof", Text: "proof"})
				f.do(t, &core.Op{Kind: core.OpSpaceJoin, Token: f.token, Space: "fresh-proof"})
			}
			for i := range 2 {
				var serial uint64
				if kind == "announcement" {
					serial = f.do(t, &core.Op{Kind: core.OpSpaceAnnounce, Token: f.sender, Space: "fresh-proof", Body: "fresh announcement"})["serial"].(uint64)
				} else {
					serial = f.do(t, &core.Op{Kind: core.OpSendMessage, Token: f.token, To: "sender", MsgType: core.MsgQuestion, Body: "question"})["msg_serial"].(uint64)
					f.do(t, &core.Op{Kind: core.OpRespond, Token: f.sender, MsgSerial: serial, Disposition: "answer", Body: "answer"})
				}
				text := f.receive(t, time.Second)
				if !strings.Contains(text, strconv.FormatUint(serial, 10)) {
					t.Fatalf("fresh %s %d not offered: %q", kind, i, text)
				}
			}
			select {
			case text := <-f.wire:
				t.Fatalf("no new %s item bought another write: %q", kind, text)
			case <-time.After(1100 * time.Millisecond):
			}
		})
	}
}

func TestSocketEconomyBusyCeilingRefreshesAtProductionActivity(t *testing.T) {
	for _, event := range []string{"authenticated call", "PreToolUse", "PostToolUse", "UserPromptSubmit", "PermissionRequest"} {
		t.Run(event, func(t *testing.T) {
			f := newDaemonEconomyFixture(t)
			f.do(t, &core.Op{Kind: core.OpAckBoard, Token: f.token})
			f.ageBusy(t, 29*time.Minute)
			before := time.Now()
			if event == "authenticated call" {
				f.do(t, &core.Op{Kind: core.OpAckBoard, Token: f.token})
			} else {
				f.hook(t, event, false)
			}
			r, err := f.e.query(f.ctx, func() core.Result {
				l := f.e.state.Agents["worker"]
				turn := f.e.socketTurns[socketSessionKey(l)]
				return core.Result{"fresh": !turn.at.Before(before), "lifecycle": f.e.socketLifecycle(l, turn.at.Add(29*time.Minute))}
			})
			if err != nil || r["fresh"] != true || r["lifecycle"] != "busy" {
				t.Fatalf("production activity did not refresh the busy ceiling: %v %v", r, err)
			}
		})
	}
}

func TestSocketEconomyDaemonFallbackUsesLifecycleAndMail(t *testing.T) {
	for _, mode := range []string{"busy-question", "busy-notify", "idle-question", "idle-notify"} {
		t.Run(mode, func(t *testing.T) {
			f := newDaemonEconomyFixture(t)
			marker := "fallback-" + mode
			busy := strings.HasPrefix(mode, "busy-")
			if busy {
				f.hook(t, "UserPromptSubmit", false)
			}
			kind := core.MsgQuestion
			if strings.HasSuffix(mode, "notify") {
				kind = core.MsgNotify
			}
			r := f.do(t, &core.Op{Kind: core.OpSendMessage, Token: f.sender, To: "worker", MsgType: kind, Body: marker})
			if text := f.receive(t, time.Second); !strings.Contains(text, marker) {
				t.Fatalf("daemon lost actionable mail during %s: %q", mode, text)
			}
			got := f.hook(t, "Stop", false)
			if got["decision"] != "block" || !strings.Contains(fmtResult(got), marker) {
				t.Fatalf("authored mail lost its blocking Stop fallback: %v", got)
			}
			select {
			case text := <-f.wire:
				t.Fatalf("Stop delivery wrote an extra native frame: %q", text)
			case <-time.After(1100 * time.Millisecond):
			}
			_, err := f.e.query(f.ctx, func() core.Result {
				if f.e.state.Messages[r["msg_serial"].(uint64)].Consumed {
					t.Error("native/hook presentation consumed raw mail")
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSocketEconomyAnnouncementWritesAndAcknowledgmentQuietsBothRoutes(t *testing.T) {
	f := newDaemonEconomyFixture(t)
	f.do(t, &core.Op{Kind: core.OpAckBoard, Token: f.sender})
	f.do(t, &core.Op{Kind: core.OpAckBoard, Token: f.token})
	f.do(t, &core.Op{Kind: core.OpSpaceOpen, Token: f.sender, Space: "announcement-proof", Text: "proof"})
	f.do(t, &core.Op{Kind: core.OpSpaceJoin, Token: f.token, Space: "announcement-proof"})
	f.do(t, &core.Op{Kind: core.OpAckBoard, Token: f.sender})
	f.hook(t, "Stop", true) // real idle boundary, no presentation
	announcement := f.do(t, &core.Op{
		Kind: core.OpSpaceAnnounce, Token: f.sender,
		Space: "announcement-proof", Body: "required acknowledgment",
	})["serial"].(uint64)
	if text := f.receive(t, 2*time.Second); !strings.Contains(text, "ack_announcement") {
		t.Fatalf("idle native route omitted required announcement: %q", text)
	}
	f.do(t, &core.Op{Kind: core.OpSpaceAck, Token: f.token, MsgSerial: announcement})
	if got := f.hook(t, "Stop", false); got["decision"] == "block" || deliveredSomething(got) {
		t.Fatalf("acknowledged announcement continued Stop: %v", got)
	}
	// The authenticated acknowledgment entered a new busy epoch and cleared
	// the old written reservation. A quiet offer cannot pass by inheriting it.
	offer, err := f.e.SocketOfferFor(f.ctx, f.token, f.sid, "", false)
	if err != nil || offer["digest"] != "" {
		t.Fatalf("acknowledged announcement offered another native wake: %v %v", offer, err)
	}
	select {
	case text := <-f.wire:
		t.Fatalf("acknowledged announcement wrote again: %q", text)
	case <-time.After(1100 * time.Millisecond):
	}
}

func TestSocketEconomyDaemonFailureKeepsOneRetryAndNoFalseTurn(t *testing.T) {
	f := newDaemonEconomyFixture(t)
	f.hook(t, "UserPromptSubmit", false)
	f.e.SetWakeCommands(map[string]WakeCommand{"claude code": {Argv: []string{"/usr/bin/false"}, Cooldown: 200 * time.Millisecond}})
	if err := f.sock.Close(); err != nil {
		t.Fatal("setup: close receiver:", err)
	}
	f.do(t, &core.Op{Kind: core.OpSendMessage, Token: f.sender, To: "worker", MsgType: core.MsgQuestion, Body: "failed-daemon-mail"})
	f.hook(t, "Stop", true) // event attempts actual delivery into the missing socket
	deadline := time.Now().Add(defaultPeerCooldown + 3*time.Second)
	armed := false
	for {
		r, err := f.e.query(f.ctx, func() core.Result {
			f.e.wakers.mu.Lock()
			pending := f.e.wakers.deferred["worker"] != nil
			f.e.wakers.mu.Unlock()
			return core.Result{"pending": pending, "failures": f.e.socketFailures[socketSessionKey(f.e.state.Agents["worker"])].count}
		})
		if err != nil {
			t.Fatal(err)
		}
		armed = armed || r["pending"] == true
		if armed && r["pending"] == false {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("failed native offer did not arm/settle its one retry", r)
		}
		<-time.After(10 * time.Millisecond)
	}

	<-time.After(100 * time.Millisecond)
	r, err := f.e.query(f.ctx, func() core.Result { return core.Result{"offers": f.e.nextSocketOffer} })
	if err != nil || r["offers"] != uint64(1) {
		t.Fatalf("failure path unexpectedly wrote another native offer: %v %v", r, err)
	}
	f.e.wakers.mu.Lock()
	_, started := f.e.wakers.dibsTurn["worker"]
	f.e.wakers.mu.Unlock()
	if started {
		t.Fatal("failed native delivery invented a turn")
	}
	if text := fmtResult(f.hook(t, "Stop", false)); !strings.Contains(text, "failed-daemon-mail") {
		t.Fatalf("lost hook fallback: %s", text)
	}
}

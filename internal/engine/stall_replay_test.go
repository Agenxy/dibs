package engine

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/ledger"
)

// Enter at the real declaration and tick doors. Restart from serialized
// ledger operations, never from a copy of live state or a manually set flag.
func TestStallNoticeSurvivesReplayAndNoticeRetention(t *testing.T) {
	b := newStallReplayBoard(t)
	b.declare("waiting on the review")
	b.exhaust()
	notice := b.waitNotice(1)
	if !strings.Contains(notice.Body, fmt.Sprintf("respond(msg_serial:%d, disposition:\"withdraw\"", b.request)) {
		t.Errorf("requester has no exact withdrawal lever: %s", notice.Body)
	}
	b.do(&core.Op{Kind: core.OpAckMessage, Token: b.lead, MsgSerial: notice.Serial})
	b.query(func() core.Result {
		_, err := b.e.exec(&core.Op{Kind: core.OpSweep, PurgeMail: true, KeepOwed: true, V7Semantics: true}, b.now.Add(time.Hour))
		if err != nil {
			b.t.Error(err)
		}
		return core.Result{"retained": b.e.state.Messages[notice.Serial] != nil}
	})
	// A notice may be retained for other reasons; explicitly prove purge here.
	if b.query(func() core.Result { return core.Result{"retained": b.e.state.Messages[notice.Serial] != nil} })["retained"] == true {
		t.Fatal("setup: acknowledged notice was not purged")
	}
	b.restart()
	b.exhaust()
	b.assertNoNotice()
	b.declare("review moved, waiting on another check")
	b.exhaust()
	b.waitNotice(1)
	b.restart()
	b.exhaust()
	b.assertNoticeCount(1)
}

func TestWithdrawnApprovedRequestProducesNoFurtherStallNotice(t *testing.T) {
	b := newStallReplayBoard(t)
	b.declare("waiting on the review")
	b.exhaust()
	b.waitNotice(1)
	r := b.do(&core.Op{Kind: core.OpRespond, Token: b.lead, MsgSerial: b.request, Disposition: "withdraw", Body: "someone else finished"})
	if r["state"] != core.MsgStateWithdrawn {
		t.Fatalf("setup: withdrawal: %v", r)
	}
	b.restart()
	b.exhaust()
	b.assertNoticeCount(1)
}

type stallReplayBoard struct {
	t            *testing.T
	e            *Engine
	ctx          context.Context
	stop         func()
	led          *ledger.Ledger
	path         string
	box          *ledger.Box
	lead, worker string
	request      uint64
	now          time.Time
}

func newStallReplayBoard(t *testing.T) *stallReplayBoard {
	t.Helper()
	dir := t.TempDir()
	box, err := ledger.LoadOrCreateKey(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal("setup key:", err)
	}
	b := &stallReplayBoard{t: t, box: box, path: filepath.Join(dir, "ledger.jsonl"), now: time.Now().Add(time.Hour)}
	b.start(core.NewState("stall-replay", core.DefaultLimits()))
	t.Cleanup(func() { b.stop() })
	b.lead = b.do(&core.Op{Kind: core.OpRegister, Name: "lead", Nonce: "stall-replay-lead", AgentKind: core.KindPersistent})["token"].(string)
	b.worker = b.do(&core.Op{Kind: core.OpRegister, Name: "worker", Nonce: "stall-replay-worker", AgentKind: core.KindPersistent})["token"].(string)
	b.do(&core.Op{Kind: core.OpAckBoard, Token: b.lead})
	b.do(&core.Op{Kind: core.OpAckBoard, Token: b.worker})
	b.request = b.do(&core.Op{Kind: core.OpSendMessage, Token: b.lead, To: "worker", MsgType: core.MsgRequest, Body: "build it"})["msg_serial"].(uint64)
	b.do(&core.Op{Kind: core.OpRespond, Token: b.worker, MsgSerial: b.request, Disposition: "approve"})
	return b
}

func (b *stallReplayBoard) start(st *core.State) {
	var err error
	b.led, err = ledger.Open(b.path, "stall-replay", b.box)
	if err != nil {
		b.t.Fatal("setup ledger:", err)
	}
	if _, err := b.led.Replay(st); err != nil {
		b.t.Fatal("real encrypted replay:", err)
	}
	b.e = New(st, b.led, deadProber{})
	b.ctx, b.stop = context.WithCancel(context.Background())
	cancel := b.stop
	done := make(chan struct{})
	go func() { b.e.Run(b.ctx); close(done) }()
	b.stop = func() {
		cancel()
		<-done
		if err := b.led.Close(); err != nil {
			b.t.Error(err)
		}
	}
}

func (b *stallReplayBoard) restart() {
	b.t.Helper()
	b.stop()
	b.start(core.NewState("stall-replay", core.DefaultLimits()))
}

func (b *stallReplayBoard) do(op *core.Op) core.Result {
	b.t.Helper()
	r, err := b.e.Do(b.ctx, op)
	if err != nil {
		b.t.Fatalf("setup %s: %v", op.Kind, err)
	}
	return r
}

func (b *stallReplayBoard) query(fn func() core.Result) core.Result {
	b.t.Helper()
	r, err := b.e.query(b.ctx, fn)
	if err != nil {
		b.t.Fatal(err)
	}
	return r
}

func (b *stallReplayBoard) declare(text string) {
	b.do(&core.Op{
		Kind: core.OpSetSlot, Token: b.worker, SlotID: "s1", Text: text,
		Waiting: "ci", RecheckSec: 1, Refs: []string{fmt.Sprintf("request:%d", b.request)},
	})
}

func (b *stallReplayBoard) exhaust() {
	b.t.Helper()
	b.do(&core.Op{Kind: core.OpSweep, DeadAgents: []string{"worker"}})
	for range maxRechecks + 2 {
		b.now = b.now.Add(time.Minute)
		b.query(func() core.Result { b.e.stallTick(b.now); return nil })
	}
	work := b.query(func() core.Result { return core.Result{"work": b.e.workStateOf(b.e.state.Agents["worker"])} })["work"]
	if work != "stalled" {
		b.t.Fatalf("setup: real tick did not reach stalled: %v", work)
	}
}

func (b *stallReplayBoard) notices() []*core.Message {
	r := b.query(func() core.Result {
		var notices []*core.Message
		for _, m := range b.e.state.Messages {
			if m.To == "lead" && m.Type == core.MsgNotify && strings.Contains(m.Body, "has stalled") {
				copyOf := *m
				notices = append(notices, &copyOf)
			}
		}
		return core.Result{"notices": notices}
	})
	return r["notices"].([]*core.Message)
}

func (b *stallReplayBoard) waitNotice(want int) *core.Message {
	b.t.Helper()
	for range 200 {
		n := b.notices()
		if len(n) == want {
			return n[0]
		}
		<-time.After(5 * time.Millisecond)
	}
	b.t.Fatalf("notice count %d, want %d", len(b.notices()), want)
	return nil
}

func (b *stallReplayBoard) assertNoNotice() { b.assertNoticeCount(0) }

func (b *stallReplayBoard) assertNoticeCount(want int) {
	b.t.Helper()
	// Settle the asynchronous reporter; a zero before it ran proves nothing.
	<-time.After(100 * time.Millisecond)
	if n := len(b.notices()); n != want {
		b.t.Errorf("stall notices after replay: %d, want %d", n, want)
	}
}

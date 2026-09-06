package mcp

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// One op emits several events at one serial, and the channel drops one
// event at a time. The first event of a check_in, a board event nobody on
// the stream wanted, was delivered and moved the position to its serial;
// the two message.delivered events after it at the same serial were
// dropped, and the refill asked for strictly later serials and recovered
// neither. The position is (serial, sub), and a refill re-reads the
// position's own serial.
//
// The pump's position is set by hand: reaching this ordering through the
// stream needs the loss to land between the pump reading one event and the
// next, which a test cannot hold open. The first version of this test went
// through the stream and passed against the bug.
func TestARefillRecoversTheRestOfAPartlyDeliveredSerial(t *testing.T) {
	srv, eng, s := newServerWithEngine(t)
	ctx := context.Background()
	busy := toolCall(t, srv, "register", map[string]any{"name": "busy", "cwd": t.TempDir()})
	helper := toolCall(t, srv, "register", map[string]any{"name": "helper", "cwd": t.TempDir()})
	for i := 0; i < 2; i++ {
		toolCall(t, srv, "send", map[string]any{"token": busy["token"], "to": "helper", "type": "question", "body": "q"})
	}
	// The check_in: board.acked first, then two message.delivered to busy,
	// all at one serial.
	ack, err := eng.Do(ctx, &core.Op{Kind: core.OpAckBoard, Token: helper["token"].(string)})
	if err != nil {
		t.Fatal("setup:", err)
	}
	serial, _ := ack["serial"].(uint64)
	res, err := eng.EventsSince(ctx, "", serial-1, true)
	if err != nil {
		t.Fatal("setup:", err)
	}
	evs, _ := res["events"].([]core.Event)
	if len(evs) < 3 || evs[0].Serial != serial || evs[0].To == "busy" || evs[1].To != "busy" || evs[1].Sub != 1 {
		t.Fatalf("setup: the check_in's events are not [board, to busy, to busy] at one serial: %v", evs)
	}

	// The pump delivered the board event and lost the rest of the serial.
	sub, cancel := eng.SubscribeTracked(serial)
	defer cancel()
	rec := httptest.NewRecorder()
	p := &pumpState{
		s: s, ctx: ctx, stream: sseStream{w: rec, fl: rec}, sub: sub,
		last: serial, lastSub: 0, subID: json.RawMessage(`"listen"`),
		wants: func() (string, bool, bool) { return "busy", true, false },
	}
	if p.sub.Lost() {
		t.Fatal("setup: the subscription reports a loss before one was recorded")
	}
	sub.MarkLost()
	if !p.refill() {
		t.Fatal("the refill reported the stream closed")
	}
	got := rec.Body.String()
	if !strings.Contains(got, `"com.dibs/serial":`+strconv.FormatUint(serial, 10)) {
		t.Fatalf("the message.delivered events at serial %d, dropped after the board event at the same "+
			"serial was delivered, were not recovered: the refill asked for later serials only:\n%s", serial, got)
	}
	if p.last != serial || p.lastSub != 2 {
		t.Errorf("after the refill the position is (%d, %d), want (%d, 2)", p.last, p.lastSub, serial)
	}
}

package mcp

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// The live channel holds 256 events and the loop drops rather than stalls
// when it is full. A resumed subscription writes its replay before it drains
// the channel, so a burst of fleet events during a slow replay filled it and
// a question that arrived after the burst was in neither the replay nor the
// stream, silently. The channel says when it dropped, and the pump refills
// from the ring.
func TestAQuestionArrivingAfterABurstDuringTheReplayIsStillDelivered(t *testing.T) {
	srv, eng, s := newServerWithEngine(t)
	ctx := context.Background()
	busy := toolCall(t, srv, "register", map[string]any{"name": "busy", "cwd": t.TempDir()})
	cursor, _ := busy["serial"].(float64)
	asker := toolCall(t, srv, "register", map[string]any{"name": "asker", "cwd": t.TempDir()})
	toolCall(t, srv, "register", map[string]any{"name": "third", "cwd": t.TempDir()})
	// One message in the gap, so the subscription resumes and replays.
	toolCall(t, srv, "send", map[string]any{"token": asker["token"], "to": "busy", "type": "notify", "body": "in the gap"})

	// While the replay is being written: a burst past the channel's buffer,
	// then the question.
	landed := make(chan uint64, 1)
	s.duringReplay = func() {
		for i := 0; i < 10; i++ {
			res, err := eng.Do(ctx, &core.Op{Kind: core.OpRegister, Name: fmt.Sprintf("sender-%d", i)})
			if err != nil {
				t.Error("setup:", err)
				close(landed)
				return
			}
			tok, _ := res["token"].(string)
			for j := 0; j < 25; j++ {
				if _, err := eng.Do(ctx, &core.Op{Kind: core.OpSendMessage, Token: tok, To: "third", MsgType: core.MsgNotify, Body: "filler"}); err != nil {
					t.Error("setup:", err)
					close(landed)
					return
				}
			}
			for j := 0; j < 3; j++ {
				if _, err := eng.Do(ctx, &core.Op{Kind: core.OpUpdate, Token: tok, Description: fmt.Sprintf("d%d", j)}); err != nil {
					t.Error("setup:", err)
					close(landed)
					return
				}
			}
		}
		sent, err := eng.Do(ctx, &core.Op{Kind: core.OpSendMessage, Token: asker["token"].(string), To: "busy", MsgType: core.MsgQuestion, Body: "after the burst", DeadlineSec: 600})
		if err != nil {
			t.Error("setup:", err)
			close(landed)
			return
		}
		serial, _ := sent["msg_serial"].(uint64)
		landed <- serial
	}
	lines := openListen(t, srv, busy["token"].(string), cursor)
	var serial float64
	select {
	case n, ok := <-landed:
		if !ok || n == 0 {
			t.Fatal("setup: the question was not sent inside the replay")
		}
		serial = float64(n)
	case <-time.After(10 * time.Second):
		t.Fatal("setup: the replay seam never ran")
	}
	if serial-cursor <= 256 {
		t.Fatalf("setup: only %v event(s) between the cursor and the question; the buffer holds 256", serial-cursor)
	}
	if !awaitInboxSerial(lines, serial, 5*time.Second) {
		t.Fatal("a question that arrived after a burst filled the channel during the replay never " +
			"reached the stream: dropped by the channel and refilled by nobody")
	}
}

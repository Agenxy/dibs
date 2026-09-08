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

func newServerWithEngine(t *testing.T) (*httptest.Server, *engine.Engine, *Server) {
	t.Helper()
	dir := t.TempDir()
	box, err := ledger.LoadOrCreateKey(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	led, err := ledger.Open(filepath.Join(dir, "ledger.jsonl"), "test", box)
	if err != nil {
		t.Fatal(err)
	}
	st := core.NewState("test", core.DefaultLimits())
	if _, err := led.Replay(st); err != nil {
		t.Fatal(err)
	}
	eng := engine.New(st, led, nil)
	ctx, cancel := context.WithCancel(context.Background())
	go eng.Run(ctx)
	s := New(eng)
	srv := httptest.NewServer(s)
	t.Cleanup(func() { srv.Close(); cancel(); _ = led.Close() })
	return srv, eng, s
}

// awaitInboxSerial reads the stream until an inbox notice carrying serial
// arrives, or the deadline passes.
func awaitInboxSerial(lines <-chan string, serial float64, within time.Duration) bool {
	deadline := time.After(within)
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				return false
			}
			data, found := strings.CutPrefix(line, "data: ")
			if !found {
				continue
			}
			var frame struct {
				Method string `json:"method"`
				Params struct {
					URI  string         `json:"uri"`
					Meta map[string]any `json:"_meta"`
				} `json:"params"`
			}
			if json.Unmarshal([]byte(data), &frame) != nil {
				continue
			}
			if frame.Method == "notifications/resources/updated" && frame.Params.URI == "dibs://inbox" {
				if got, _ := frame.Params.Meta[SerialMetaKey].(float64); got == serial {
					return true
				}
			}
		case <-deadline:
			return false
		}
	}
}

// The live channel was opened after the gap was replayed, and from the
// cursor: its catch-up pushed the whole gap into a buffer that drops when
// full, so a long gap filled it with history already replayed and a
// question sent while the replay was being written landed past the buffer,
// in neither the replay nor the stream. The channel opens first, from the
// present, and buffers what arrives while the replay runs.
//
// The arrival is PUT in the window, through the server's replay seam, rather
// than sent after the listen opened and hoped to land there: the first
// version of this test did the latter, and against the old order it passed
// whenever the replay finished first. Found by the pre-release review, round
// thirty-two.
func TestAQuestionSentDuringAReplayReachesTheStream(t *testing.T) {
	srv, eng, s := newServerWithEngine(t)
	ctx := context.Background()
	busy := toolCall(t, srv, "register", map[string]any{"name": "busy", "cwd": t.TempDir()})
	cursor, _ := busy["serial"].(float64)
	asker := toolCall(t, srv, "register", map[string]any{"name": "asker", "cwd": t.TempDir()})
	// A gap longer than the channel's buffer, most of it mail to busy, so the
	// replay has real work and the question below lands while it runs. The
	// mailbox keeps room for that question.
	for i := 0; i < 10; i++ {
		res, err := eng.Do(ctx, &core.Op{Kind: core.OpRegister, Name: fmt.Sprintf("sender-%d", i)})
		if err != nil {
			t.Fatal("setup:", err)
		}
		tok, _ := res["token"].(string)
		for j := 0; j < 25; j++ {
			if _, err := eng.Do(ctx, &core.Op{Kind: core.OpSendMessage, Token: tok, To: "busy", MsgType: core.MsgNotify, Body: "filler"}); err != nil {
				t.Fatal("setup:", err)
			}
		}
		for j := 0; j < 3; j++ {
			if _, err := eng.Do(ctx, &core.Op{Kind: core.OpUpdate, Token: tok, Description: fmt.Sprintf("d%d", j)}); err != nil {
				t.Fatal("setup:", err)
			}
		}
	}
	_, now, err := eng.SubscribeInfo(ctx, "")
	if err != nil {
		t.Fatal("setup:", err)
	}
	if gap := now - uint64(cursor); gap <= 256 {
		t.Fatalf("setup: the gap holds %d event(s), not past the 256 buffer", gap)
	}
	// The question lands after the gap was read and before it is written.
	landed := make(chan uint64, 1)
	s.duringReplay = func() {
		sent, err := eng.Do(ctx, &core.Op{Kind: core.OpSendMessage, Token: asker["token"].(string), To: "busy", MsgType: core.MsgQuestion, Body: "sent during the replay", DeadlineSec: 600})
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
	case <-time.After(5 * time.Second):
		t.Fatal("setup: the replay seam never ran, so nothing was sent inside the window")
	}
	if !awaitInboxSerial(lines, serial, 5*time.Second) {
		t.Fatal("a question sent while the reconnect's gap was being replayed never reached the " +
			"stream: past the buffer, in neither the replay nor the live channel")
	}
}

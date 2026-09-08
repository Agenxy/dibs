package mcp

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/ledger"
)

// newSmallRingServer is newServer with an event ring of four, so the floor
// rises the way it does after a long run without generating 65,536 events.
func newSmallRingServer(t *testing.T) *httptest.Server {
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
	eng.SetRingCap(4)
	ctx, cancel := context.WithCancel(context.Background())
	go eng.Run(ctx)
	srv := httptest.NewServer(New(eng))
	t.Cleanup(func() { srv.Close(); cancel(); _ = led.Close() })
	return srv
}

// awaitInboxNotice returns the _meta of the first inbox notification on the
// stream, or nil.
func awaitInboxNotice(lines <-chan string) map[string]any {
	deadline := time.After(2 * time.Second)
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				return nil
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
				return frame.Params.Meta
			}
		case <-deadline:
			return nil
		}
	}
}

// A subscriber whose cursor is older than the ring got an empty replay: a
// question that arrived while it was away, whose event had since left the
// ring, sat in its inbox with no notice and no signal that one was missed.
// The inbox says what is still owed, and each waiting message after the
// cursor is replayed as the notice the ring would have carried.
func TestAReconnectPastTheRingIsResyncedFromTheInbox(t *testing.T) {
	srv := newSmallRingServer(t)
	asker := toolCall(t, srv, "register", map[string]any{"name": "asker", "cwd": t.TempDir()})
	busy := toolCall(t, srv, "register", map[string]any{"name": "busy", "cwd": t.TempDir()})
	toolCall(t, srv, "register", map[string]any{"name": "third", "cwd": t.TempDir()})
	cursor, _ := busy["serial"].(float64)
	sent := toolCall(t, srv, "send", map[string]any{
		"token": asker["token"], "to": "busy", "type": "question", "body": "while you were away",
	})
	serial, _ := sent["msg_serial"].(float64)
	if serial == 0 || cursor == 0 {
		t.Fatalf("setup: no serials: %v %v", sent, busy)
	}
	// Push the ring past the question, so the gap cannot be replayed from it.
	for i := 0; i < 8; i++ {
		toolCall(t, srv, "send", map[string]any{
			"token": asker["token"], "to": "third", "type": "notify", "body": "filler",
		})
	}
	stale := toolCall(t, srv, "events_since", map[string]any{"token": busy["token"], "since_serial": cursor})
	if code, _ := stale["code"].(string); code != "E_CURSOR_TOO_OLD" {
		t.Fatalf("setup: the ring still holds the cursor, so the resync below proves nothing: %v", stale)
	}
	meta := awaitInboxNotice(openListen(t, srv, busy["token"].(string), cursor))
	if meta == nil {
		t.Fatal("a subscriber that reconnected past the ring was handed nothing: the question " +
			"in its inbox wakes nobody until something else arrives")
	}
	if got, _ := meta[SerialMetaKey].(float64); got != serial {
		t.Errorf("the resynced notice carries serial %v, want the question's %v", got, serial)
	}
	if got, _ := meta[MsgTypeMetaKey].(string); got != "question" {
		t.Errorf("the resynced notice says %q, want question: the bridge decides a wake on it", got)
	}
}

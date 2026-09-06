package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func openListen(t *testing.T, srv *httptest.Server, token string, since any) <-chan string {
	t.Helper()
	meta := map[string]any{metaTokenKey: token}
	if since != nil {
		meta[SinceMetaKey] = since
	}
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": "listen", "method": "subscriptions/listen",
		"params": map[string]any{
			"_meta":         meta,
			"notifications": map[string]any{"resourceSubscriptions": []string{"dibs://inbox"}},
		},
	})
	req, _ := http.NewRequest(http.MethodPost, srv.URL, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	ctx, cancel := context.WithCancel(context.Background())
	resp, err := srv.Client().Do(req.WithContext(ctx))
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); _ = resp.Body.Close() })
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("listen did not open: %s", resp.Status)
	}
	lines := make(chan string, 64)
	go func() {
		defer close(lines)
		scan := bufio.NewScanner(resp.Body)
		for scan.Scan() {
			select {
			case lines <- scan.Text():
			case <-ctx.Done():
				return
			}
		}
	}()
	return lines
}

// R12-2: a subscriber that reconnects with the serial it last saw is handed
// what arrived in the gap; one that reconnects blind starts at the present.
func TestAReconnectingSubscriberIsHandedTheGap(t *testing.T) {
	srv, _ := newServer(t)
	asker := toolCall(t, srv, "register", map[string]any{"name": "asker", "cwd": t.TempDir()})
	busy := toolCall(t, srv, "register", map[string]any{"name": "busy", "cwd": t.TempDir()})
	sent := toolCall(t, srv, "send", map[string]any{
		"token": asker["token"], "to": "busy", "type": "question", "body": "while you were away",
	})
	serial, _ := sent["msg_serial"].(float64)
	if serial == 0 {
		t.Fatalf("setup: the send reported no serial: %v", sent)
	}
	// Blind: nothing arrives, because the subscription starts now.
	if awaitUpdate(openListen(t, srv, busy["token"].(string), nil), "dibs://inbox") {
		t.Fatal("setup: a fresh subscription replayed the past on its own, so the cursor below proves nothing")
	}
	// With the cursor from before the send: the missed message is replayed.
	if !awaitUpdate(openListen(t, srv, busy["token"].(string), serial-1), "dibs://inbox") {
		t.Fatal("a subscriber that said where it left off was not handed the message that " +
			"arrived in the gap: an idle session sleeps on blocking mail until something else comes")
	}
	_ = time.Second
}

// The acknowledgment names the serial the subscription starts from.
func TestTheAcknowledgmentCarriesTheCursor(t *testing.T) {
	srv, _ := newServer(t)
	agent := toolCall(t, srv, "register", map[string]any{"name": "idle", "cwd": t.TempDir()})
	lines := openListen(t, srv, agent["token"].(string), nil)
	deadline := time.After(2 * time.Second)
	for {
		select {
		case line := <-lines:
			data, found := strings.CutPrefix(line, "data: ")
			if !found {
				continue
			}
			var frame struct {
				Method string `json:"method"`
				Params struct {
					Meta map[string]any `json:"_meta"`
				} `json:"params"`
			}
			if json.Unmarshal([]byte(data), &frame) != nil || frame.Method != "notifications/subscriptions/acknowledged" {
				continue
			}
			if v, _ := frame.Params.Meta[SerialMetaKey].(float64); v <= 0 {
				t.Fatalf("the acknowledgment carries no cursor (%v): a subscriber that drops before "+
					"its first notification reconnects blind", frame.Params.Meta)
			}
			return
		case <-deadline:
			t.Fatal("no acknowledgment arrived")
		}
	}
}

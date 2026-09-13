package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// A host bridge opens one stream naming the machine it is on and the
// harnesses its own [wake.exec] can start; the hub acknowledges exactly that
// and nothing else, and a bridge that names no host is refused: a wake
// request handed to a stream with no host would run nowhere.
func TestAHostBridgeStreamIsAcknowledgedForItsHostAlone(t *testing.T) {
	srv, _ := newServer(t)
	listen := func(meta map[string]any) (*http.Response, context.CancelFunc) {
		t.Helper()
		params := map[string]any{"notifications": map[string]any{"resourceSubscriptions": []string{WakeURI, "dibs://board"}}}
		if meta != nil {
			params["_meta"] = meta
		}
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": "wake", "method": "subscriptions/listen", "params": params})
		req, _ := http.NewRequest(http.MethodPost, srv.URL, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream")
		ctx, cancel := context.WithCancel(context.Background())
		resp, err := srv.Client().Do(req.WithContext(ctx))
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		return resp, cancel
	}

	resp, cancel := listen(nil)
	defer cancel()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("a wake stream with no host opened with %s; want it refused", resp.Status)
	}
	_ = resp.Body.Close()

	resp, cancel = listen(map[string]any{HostMetaKey: "laptop", WakeHarnessesMetaKey: []string{"Codex", "claude code"}})
	defer cancel()
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("wake stream did not open: %s", resp.Status)
	}
	ack := make(chan map[string]any, 1)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			line := sc.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var m map[string]any
			if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &m) == nil {
				ack <- m
				return
			}
		}
	}()
	select {
	case m := <-ack:
		if m["method"] != "notifications/subscriptions/acknowledged" {
			t.Fatalf("first frame = %v", m)
		}
		params, _ := m["params"].(map[string]any)
		notes, _ := params["notifications"].(map[string]any)
		subs, _ := notes["resourceSubscriptions"].([]any)
		if len(subs) != 1 || subs[0] != WakeURI {
			t.Errorf("acknowledged %v: a wake stream serves the wake feed and nothing else, "+
				"however much the request asked for", subs)
		}
		meta, _ := params["_meta"].(map[string]any)
		hs, _ := meta[WakeHarnessesMetaKey].([]any)
		if meta[HostMetaKey] != "laptop" || len(hs) != 2 {
			t.Errorf("_meta = %v: the ack should echo the host and harnesses the hub recorded", meta)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no acknowledgement on the wake stream")
	}
}

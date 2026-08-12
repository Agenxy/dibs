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

// A subscription made AFTER the stream opened must be honoured.
//
// The 2025-11-25 transport describes a standalone GET stream, and the code's own
// comment says opening it before subscribing is "fine and normal". It was not:
// the handler read the subscription once and passed those booleans into the pump
// permanently, so a client that opens its stream at startup and subscribes when
// it needs something got silence forever.
//
// This is end to end through a real server for a reason. Every existing test in
// this package pokes the legacySubs map directly, and the map was never the
// broken part: the bug lived in the wiring between the map and the pump, which
// is invisible from either side alone.
func TestSubscribingAfterTheStreamIsOpenStillDelivers(t *testing.T) {
	srv, _ := newServer(t)
	session := legacyHandshake(t, srv)

	lines, stop := openLegacyStream(t, srv, session)
	defer stop()

	// Subscribe only now, with the stream already open. This is the ordering
	// that never worked.
	legacyRPC(t, srv, session, "resources/subscribe", map[string]any{"uri": "dibs://board"})

	// Any registration changes the board.
	toolCall(t, srv, "register", map[string]any{"name": "after-open", "cwd": t.TempDir()})

	if !awaitUpdate(lines, "dibs://board") {
		t.Error("no notification for dibs://board after subscribing on an already-open stream.\n" +
			"A client that opens its stream first and subscribes later hears nothing, ever.")
	}
}

// Unsubscribing must stop an already-open stream.
//
// Same root cause as the ordering bug, opposite direction: the stream kept
// delivering what the client had explicitly asked it to stop sending.
func TestUnsubscribingStopsAnOpenStream(t *testing.T) {
	srv, _ := newServer(t)
	session := legacyHandshake(t, srv)

	legacyRPC(t, srv, session, "resources/subscribe", map[string]any{"uri": "dibs://board"})
	lines, stop := openLegacyStream(t, srv, session)
	defer stop()

	// Prove the stream works before asking it to stop, so a silent failure to
	// deliver cannot masquerade as a successful unsubscribe.
	toolCall(t, srv, "register", map[string]any{"name": "before-unsub", "cwd": t.TempDir()})
	if !awaitUpdate(lines, "dibs://board") {
		t.Fatal("the stream never delivered while subscribed, so this test proves nothing " +
			"about unsubscribing: the probe is broken, not the product")
	}

	legacyRPC(t, srv, session, "resources/unsubscribe", map[string]any{"uri": "dibs://board"})
	toolCall(t, srv, "register", map[string]any{"name": "after-unsub", "cwd": t.TempDir()})

	if awaitUpdate(lines, "dibs://board") {
		t.Error("still notified after unsubscribing; the stream ignores the client's request to stop")
	}
}

// Forgetting a session must forget what the legacy transport holds for it.
//
// A wiring test in the manner of admit_wired_test.go, because the defect was not
// a wrong `drop`: it was a correct `drop` that nothing called. Asserting on
// legacySubs alone would pass with the hook deleted, which is the shape this
// codebase keeps paying for.
func TestEvictingASessionForgetsItsLegacySubscriptions(t *testing.T) {
	// Through New, not a hand-built Server. The first version of this test set
	// onEvict itself and then asserted that onEvict worked, so it passed with
	// the production wiring deleted: it was verifying its own setup. That is the
	// same defect it exists to catch, committed in the test instead of the code.
	s := New(nil)

	first := s.sessions.create(false, nil)
	if !s.legacy.add(first, "dibs://board", "") {
		t.Fatal("could not record a subscription for the first session")
	}

	// Push it out of the store by filling the ceiling.
	for i := 0; i <= maxSessions; i++ {
		s.sessions.create(false, nil)
	}

	if got := s.legacy.get(first); got.board {
		t.Error("the evicted session still holds a legacy subscription.\n" +
			"Nothing removes these, so the map outgrows the session store's own ceiling " +
			"and keeps growing for the life of the daemon.")
	}
}

// legacyHandshake initializes a 2025-11-25 session and returns its id.
func legacyHandshake(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-11-25", "capabilities": map[string]any{},
			"clientInfo": map[string]any{"name": "legacy-test", "version": "1"},
		},
	})
	req, _ := http.NewRequest(http.MethodPost, srv.URL, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	session := resp.Header.Get("Mcp-Session-Id")
	if session == "" {
		t.Fatal("the legacy handshake returned no Mcp-Session-Id, so nothing below can subscribe")
	}
	return session
}

// legacyRPC sends one 2025-11-25 call on an established session.
func legacyRPC(t *testing.T, srv *httptest.Server, session, method string, params any) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": method, "params": params,
	})
	req, _ := http.NewRequest(http.MethodPost, srv.URL, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Mcp-Session-Id", session)
	req.Header.Set("MCP-Protocol-Version", "2025-11-25")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if e, bad := out["error"]; bad {
		t.Fatalf("%s failed: %v", method, e)
	}
}

// openLegacyStream opens the standalone GET stream and returns its lines.
func openLegacyStream(t *testing.T, srv *httptest.Server, session string) (<-chan string, func()) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Mcp-Session-Id", session)
	req.Header.Set("MCP-Protocol-Version", "2025-11-25")
	ctx, cancel := context.WithCancel(context.Background())
	resp, err := srv.Client().Do(req.WithContext(ctx))
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	// The body is a long-lived stream handed back to the caller, so it cannot be
	// deferred here. Cleanup owns the close: the returned stop func only ends the
	// stream early, and a test that forgets to call it still leaks nothing.
	t.Cleanup(func() { cancel(); _ = resp.Body.Close() })
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the notification stream did not open: %s", resp.Status)
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
	return lines, cancel
}

// awaitUpdate reports whether a resources/updated frame for uri arrives before
// the deadline. A deadline rather than a sleep: the test finishes as soon as the
// answer is known, and a slow machine does not make it flaky.
func awaitUpdate(lines <-chan string, uri string) bool {
	deadline := time.After(2 * time.Second)
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
					URI string `json:"uri"`
				} `json:"params"`
			}
			if json.Unmarshal([]byte(data), &frame) != nil {
				continue
			}
			if frame.Method == "notifications/resources/updated" && frame.Params.URI == uri {
				return true
			}
		case <-deadline:
			return false
		}
	}
}

package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"strings"
	"sync"
	"testing"
)

// A listen stream reaches the harness as JSON-RPC lines, not as SSE frames.
//
// `subscriptions/listen` (SEP-2575) answers one request with a long-lived
// stream. The bridge used to ReadAll every response, so the body never ended,
// the 75-second client timeout eventually killed it, and the harness got
// nothing: every plugin-path client silently had no push and fell back to
// polling, while a client on direct HTTP got notifications. Neither side
// reported anything.
func TestListenFramesReachTheHarnessAsJSONLines(t *testing.T) {
	sse := strings.Join([]string{
		`: keepalive`,
		``,
		`data: {"jsonrpc":"2.0","method":"notifications/subscriptions/acknowledged","params":{}}`,
		``,
		`data: {"jsonrpc":"2.0","method":"notifications/resources/updated","params":{"uri":"dibs://inbox"}}`,
		``,
	}, "\n")

	var buf bytes.Buffer
	out := &syncWriter{w: newBufWriter(&buf)}
	pumpSSE(strings.NewReader(sse), out)
	out.flush()

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2 (the keepalive comment and blanks must not be forwarded):\n%s",
			len(lines), buf.String())
	}
	for i, line := range lines {
		var msg map[string]any
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			t.Errorf("line %d is not JSON, so the harness cannot read it: %q", i, line)
			continue
		}
		if msg["jsonrpc"] != "2.0" {
			t.Errorf("line %d is not a JSON-RPC message: %q", i, line)
		}
		if strings.HasPrefix(line, "data: ") {
			t.Errorf("line %d still carries its SSE framing: %q", i, line)
		}
	}
}

// Only listen is streamed. Every other method must keep its exact
// request/response shape, because that is all the rest of MCP is.
func TestOnlyTheListenCallIsStreamed(t *testing.T) {
	for _, tc := range []struct {
		line string
		want string
	}{
		{line: `{"jsonrpc":"2.0","id":1,"method":"subscriptions/listen"}`, want: "subscriptions/listen"},
		{line: `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"board"}}`, want: "tools/call"},
		{line: `{"jsonrpc":"2.0","id":1,"method":"resources/read"}`, want: "resources/read"},
		{line: `not json at all`, want: ""},
	} {
		if got := methodOf([]byte(tc.line)); got != tc.want {
			t.Errorf("methodOf(%.40s) = %q, want %q", tc.line, got, tc.want)
		}
	}
}

// stdout is one file descriptor shared by the request loop and every stream.
//
// Interleaved writes produce half-lines, which read as a protocol bug rather
// than as a bridge bug: the harness sees malformed JSON from a server that is
// behaving correctly.
func TestConcurrentWritersNeverInterleaveALine(t *testing.T) {
	var buf bytes.Buffer
	out := &syncWriter{w: newBufWriter(&buf)}
	payload := []byte(`{"jsonrpc":"2.0","method":"notifications/resources/updated","params":{"uri":"dibs://board"}}`)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out.line(payload)
		}()
	}
	wg.Wait()
	out.flush()

	for i, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if !json.Valid([]byte(line)) {
			t.Fatalf("line %d is torn, so two writers overlapped: %q", i, line)
		}
	}
}

// newBufWriter adapts a buffer for syncWriter in tests.
func newBufWriter(b *bytes.Buffer) *bufio.Writer { return bufio.NewWriter(b) }

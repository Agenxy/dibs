package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
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

// A call issued while the daemon is restarting must survive it.
//
// Dibs is for long-running agents, so an upgrade cannot be a thing that costs
// them their in-flight work: an operator who watches one update break a fleet
// will stay on an old build to avoid repeating it, which is how a coordination
// service ends up permanently out of date. Nothing is lost across a restart
// (state IS the ledger, and the daemon replays it in milliseconds), but the
// call in flight used to hard-fail with "connection refused".
//
// Only a refused dial is waited out, and that is the whole safety argument: it
// means the request never reached the daemon, so re-sending it cannot duplicate
// an effect. Anything the daemon may have received is returned untouched.
func TestOnlyAnUnreachedRequestIsWaitedOut(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{name: "connection refused: never arrived", err: syscall.ECONNREFUSED, want: true},
		// A reset says the peer tore the connection down and NOTHING about how
		// much it had already done. The daemon can read, apply and ledger an op
		// and be killed before the response leaves. This case read "listener
		// went away" and wanted a retry, which assumed the half of the story
		// that suits us; retrying there duplicates the effect.
		{name: "connection reset: may have been received and acted on", err: syscall.ECONNRESET, want: false},
		{name: "wrapped refusal", err: fmt.Errorf("post: %w", syscall.ECONNREFUSED), want: true},
		{name: "timeout: may have been received and acted on", err: os.ErrDeadlineExceeded, want: false},
		{name: "no error", err: nil, want: false},
		{name: "anything else", err: errors.New("tls: bad record"), want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := dialFailed(tc.err); got != tc.want {
				t.Errorf("dialFailed(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// The grace window is bounded: a daemon that is gone for good must surface as
// an error, not as an agent hanging forever on a machine with no daemon.
func TestTheRestartGraceIsBounded(t *testing.T) {
	if upgradeGrace <= 0 {
		t.Fatal("no grace window: an upgrade breaks every call in flight")
	}
	if upgradeGrace > time.Minute {
		t.Errorf("grace of %v: a daemon that is never coming back leaves the agent hanging "+
			"with no error to act on", upgradeGrace)
	}
}

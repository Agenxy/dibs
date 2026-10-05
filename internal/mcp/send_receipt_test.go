package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/humanask"
	"github.com/agenxy/dibs/internal/ledger"
	"github.com/agenxy/dibs/internal/notify"
)

// Block only AFTER the real encrypted ledger has appended and fsynced the
// send. The request enters through real HTTP; no flag pretends it committed.
// This is the indeterminate-outcome window, not a persistence failure.
type sendReceiptLedger struct {
	*ledger.Ledger
	armed   atomic.Bool
	appends atomic.Int64
	entered chan uint64
	release chan struct{}
	once    sync.Once
}

func (l *sendReceiptLedger) Append(serial uint64, now time.Time, op *core.Op) error {
	if err := l.Ledger.Append(serial, now, op); err != nil {
		return err
	}
	l.appends.Add(1)
	if op.Kind == core.OpSendMessage && l.armed.CompareAndSwap(true, false) {
		l.entered <- serial
		<-l.release
	}
	return nil
}

func (l *sendReceiptLedger) unblock() { l.once.Do(func() { close(l.release) }) }

func sendReceiptServer(t *testing.T, dir string, configure ...func(*engine.Engine)) (*httptest.Server, *sendReceiptLedger, func()) {
	t.Helper()
	box, err := ledger.LoadOrCreateKey(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal("setup key:", err)
	}
	journal, err := ledger.Open(filepath.Join(dir, "ledger.jsonl"), "fixture", box)
	if err != nil {
		t.Fatal("setup ledger:", err)
	}
	st := core.NewState("fixture", core.DefaultLimits())
	if _, err = journal.Replay(st); err != nil {
		_ = journal.Close()
		t.Fatal("setup replay:", err)
	}
	gate := &sendReceiptLedger{Ledger: journal, entered: make(chan uint64, 1), release: make(chan struct{})}
	eng := engine.New(st, gate, nil)
	for _, option := range configure {
		option(eng)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { eng.Run(ctx); close(done) }()
	srv := httptest.NewServer(New(eng))
	var once sync.Once
	stop := func() {
		once.Do(func() {
			gate.unblock() // a failed assertion must not hang server/engine cleanup
			srv.Close()
			cancel()
			<-done
			if err := journal.Close(); err != nil {
				t.Error("close ledger:", err)
			}
		})
	}
	t.Cleanup(stop)
	return srv, gate, stop
}

type sendReceiptReply struct {
	payload map[string]any
	isError bool
	err     error
}

// Unlike toolCall helpers, this preserves the MCP isError bit and bounds the
// CLIENT only as a failing probe limit. The product must answer first.
func sendReceiptHTTP(ctx context.Context, srv *httptest.Server, version string, args map[string]any) sendReceiptReply {
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": "send", "arguments": args},
	})
	if err != nil {
		return sendReceiptReply{err: err}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL, bytes.NewReader(body))
	if err != nil {
		return sendReceiptReply{err: err}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("MCP-Protocol-Version", version)
	resp, err := srv.Client().Do(req)
	if err != nil {
		return sendReceiptReply{err: err}
	}
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
		Error json.RawMessage `json:"error"`
	}
	if err = json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return sendReceiptReply{err: err}
	}
	if resp.StatusCode != http.StatusOK || len(out.Error) != 0 || len(out.Result.Content) != 1 || out.Result.Content[0].Type != "text" {
		return sendReceiptReply{err: fmt.Errorf("HTTP%d did not carry one tool reply: %+v", resp.StatusCode, out)}
	}
	var payload map[string]any
	if err = json.Unmarshal([]byte(out.Result.Content[0].Text), &payload); err != nil {
		return sendReceiptReply{err: err}
	}
	return sendReceiptReply{payload: payload, isError: out.Result.IsError}
}

func sendReceiptSetup(t *testing.T, srv *httptest.Server, version string) string {
	t.Helper()
	if version == "2025-11-25" {
		out := rpc(t, srv, "", "initialize", map[string]any{"protocolVersion": version})
		if result(t, out, "setup legacy handshake")["protocolVersion"] != version {
			t.Fatal("setup legacy handshake did not select requested version")
		}
	}
	tokens := map[string]string{}
	for _, name := range []string{"sender", "worker"} {
		r := toolCallOn(t, srv, version, "register", map[string]any{"name": name, "nonce": "send-receipt-" + name})
		token, ok := r["token"].(string)
		if !ok || token == "" || r["code"] != nil {
			t.Fatalf("setup register %s: %v", name, r)
		}
		if r = toolCallOn(t, srv, version, "check_in", map[string]any{"token": token}); r["code"] != nil {
			t.Fatalf("setup check_in %s: %v", name, r)
		}
		tokens[name] = token
	}
	return tokens["sender"]
}

func TestSendUnknownHasServerBudgetAndDurableRetryThroughMCP(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		t.Run(version, func(t *testing.T) {
			dir := t.TempDir()
			srv, gate, stop := sendReceiptServer(t, dir)
			sender := sendReceiptSetup(t, srv, version)
			args := map[string]any{"token": sender, "to": "worker", "type": "notify", "body": "one durable message", "op_id": "bounded-retry"}
			gate.armed.Store(true)
			ctx, cancel := context.WithTimeout(context.Background(), 6500*time.Millisecond)
			defer cancel()
			answer := make(chan sendReceiptReply, 1)
			started := time.Now()
			go func() { answer <- sendReceiptHTTP(ctx, srv, version, args) }()
			var serial uint64
			select {
			case serial = <-gate.entered:
			case reply := <-answer:
				t.Fatalf("setup: send bypassed the real fsynced ledger gate: %+v", reply)
			case <-time.After(3 * time.Second):
				t.Fatal("setup: send never reached the real fsynced ledger gate")
			}
			reply := <-answer
			if reply.err != nil {
				t.Fatalf("send waited for the client deadline instead of a bounded server UNKNOWN: %v", reply.err)
			}
			if !reply.isError || reply.payload["code"] != "E_SEND_OUTCOME_UNKNOWN" || reply.payload["op_id"] != args["op_id"] {
				t.Fatalf("indeterminate send lost its structured UNKNOWN/op_id: %+v", reply)
			}
			hint, _ := reply.payload["hint"].(string)
			if !strings.Contains(hint, "same op_id") || !strings.Contains(hint, "same payload") || time.Since(started) >= 6500*time.Millisecond {
				t.Fatalf("UNKNOWN omitted exact safe retry or exceeded probe limit: %v", reply.payload)
			}
			gate.unblock()
			before := gate.appends.Load()
			retry := sendReceiptHTTP(context.Background(), srv, version, args)
			assertSendReceiptRetry(t, retry, serial)
			if got := gate.appends.Load(); got != before {
				t.Fatalf("same op_id retry appended another op: %d -> %d", before, got)
			}
			stop()
			srv, gate, _ = sendReceiptServer(t, dir)
			before = gate.appends.Load()
			assertSendReceiptRetry(t, sendReceiptHTTP(context.Background(), srv, version, args), serial)
			if got := gate.appends.Load(); got != before {
				t.Fatalf("replayed op_id retry appended another op: %d -> %d", before, got)
			}
			args["body"] = "a different payload"
			conflict := sendReceiptHTTP(context.Background(), srv, version, args)
			if conflict.err != nil || !conflict.isError || conflict.payload["code"] != "E_OP_ID_CONFLICT" {
				t.Fatalf("retry id accepted a different payload: %+v", conflict)
			}
		})
	}
}

func assertSendReceiptRetry(t *testing.T, r sendReceiptReply, serial uint64) {
	t.Helper()
	if r.err != nil || r.isError || r.payload["ok"] != true || r.payload["deduplicated"] != true || r.payload["msg_serial"] != float64(serial) {
		t.Fatalf("same op_id did not return the original accepted serial: %+v, expected %d", r, serial)
	}
}

// Available is the production route-advisory port, reached by dispatchHuman
// after the real ledger append. Blocking it does not fake an accepted send or
// call a test-only setter: the whole request still enters through HTTP.
type slowSendAdvisory struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (n *slowSendAdvisory) Available() bool {
	n.once.Do(func() { close(n.entered) })
	<-n.release
	return false
}

func (*slowSendAdvisory) Ask(humanask.Message) (humanask.Answer, error) {
	return humanask.Answer{}, nil
}

func TestSendKnownReceiptSurvivesSlowRouteAdvisoryThroughMCP(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		t.Run(version, func(t *testing.T) {
			srv, eng, _ := newServerWithEngine(t)
			n := &slowSendAdvisory{entered: make(chan struct{}), release: make(chan struct{})}
			var unblock sync.Once
			release := func() { unblock.Do(func() { close(n.release) }) }
			t.Cleanup(release) // before fixture cleanup, even on a failed assertion
			eng.SetHumanNotifier(n)
			sender := sendReceiptSetup(t, srv, version)
			args := map[string]any{"token": sender, "to": "human", "type": "question", "body": "slow route advice", "op_id": "known-receipt"}
			ctx, cancel := context.WithTimeout(context.Background(), 6500*time.Millisecond)
			defer cancel()
			answer := make(chan sendReceiptReply, 1)
			go func() { answer <- sendReceiptHTTP(ctx, srv, version, args) }()
			select {
			case <-n.entered:
			case reply := <-answer:
				t.Fatalf("setup: send never entered the production route advisory: %+v", reply)
			case <-time.After(3 * time.Second):
				t.Fatal("setup: route advisory was never reached")
			}
			reply := <-answer
			if reply.err != nil || reply.isError || reply.payload["ok"] != true || reply.payload["msg_serial"] == nil {
				t.Fatalf("slow advisory stole the already durable receipt: %+v", reply)
			}
			if reply.payload["human_route"] != nil || reply.payload["note"] != nil || reply.payload["advisories"] == nil {
				t.Fatalf("bounded receipt invented an unfinished delivery advisory: %v", reply.payload)
			}
			release()
			serial, ok := reply.payload["msg_serial"].(float64)
			if !ok {
				t.Fatalf("accepted serial has wrong shape: %v", reply.payload)
			}
			assertSendReceiptRetry(t, sendReceiptHTTP(context.Background(), srv, version, args), uint64(serial))
		})
	}
}

func TestSendUnknownWithoutIDDoesNotPromiseDeduplicatedRetryThroughMCP(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		t.Run(version, func(t *testing.T) {
			srv, gate, _ := sendReceiptServer(t, t.TempDir())
			sender := sendReceiptSetup(t, srv, version)
			args := map[string]any{"token": sender, "to": "worker", "type": "notify", "body": "unidentified send"}
			gate.armed.Store(true)
			ctx, cancel := context.WithTimeout(context.Background(), 6500*time.Millisecond)
			defer cancel()
			answer := make(chan sendReceiptReply, 1)
			go func() { answer <- sendReceiptHTTP(ctx, srv, version, args) }()
			select {
			case <-gate.entered:
			case reply := <-answer:
				t.Fatalf("setup: unidentified send bypassed the real ledger gate: %+v", reply)
			case <-time.After(3 * time.Second):
				t.Fatal("setup: unidentified send did not reach ledger gate")
			}
			reply := <-answer
			if reply.err != nil || !reply.isError || reply.payload["code"] != "E_SEND_OUTCOME_UNKNOWN" || reply.payload["op_id"] != "" {
				t.Fatalf("unidentified send did not report UNKNOWN: %+v", reply)
			}
			hint, _ := reply.payload["hint"].(string)
			if !strings.Contains(hint, "safe deduplicated retry is unavailable") || !strings.Contains(hint, "new op_id cannot identify") {
				t.Fatalf("unidentified send invented safe retry: %v", reply.payload)
			}
			gate.unblock()
		})
	}
}

type sendTimingLog struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *sendTimingLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *sendTimingLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func TestSendStageTimingIsDebugOnlyAndPrivateThroughMCP(t *testing.T) {
	for _, level := range []slog.Level{slog.LevelInfo, slog.LevelDebug} {
		t.Run(level.String(), func(t *testing.T) {
			var log sendTimingLog
			original := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&log, &slog.HandlerOptions{Level: level})))
			t.Cleanup(func() { slog.SetDefault(original) })
			srv, _, _ := newServerWithEngine(t)
			sender := sendReceiptSetup(t, srv, "2026-07-28")
			args := map[string]any{"token": sender, "to": "worker", "type": "notify", "body": "private-stage-body-31653", "op_id": "private-stage-id-31653"}
			reply := sendReceiptHTTP(context.Background(), srv, "2026-07-28", args)
			if reply.err != nil || reply.isError || reply.payload["ok"] != true {
				t.Fatalf("setup: healthy send failed: %+v", reply)
			}
			raw := log.String()
			if level == slog.LevelInfo {
				if strings.Contains(raw, "send stage") {
					t.Fatalf("stage timing escaped Debug level: %s", raw)
				}
				return
			}
			stages := map[string]bool{}
			for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
				if strings.TrimSpace(line) == "" {
					continue
				}
				var event map[string]any
				if err := json.Unmarshal([]byte(line), &event); err != nil {
					t.Fatalf("timing log was not JSON: %v", err)
				}
				if event["msg"] == "send stage" && event["event"] == "complete" {
					stage, _ := event["stage"].(string)
					stages[stage] = event["elapsed"] != nil && event["level"] == "DEBUG"
				}
			}
			for _, stage := range []string{"admit", "apply", "ledger append", "publish", "advisories", "response"} {
				if !stages[stage] {
					t.Errorf("missing measured Debug stage %q: %s", stage, raw)
				}
			}
			for _, secret := range []string{sender, "private-stage-body-31653", "private-stage-id-31653"} {
				if strings.Contains(raw, secret) {
					t.Error("send timing leaked a body or credential")
				}
			}
		})
	}
}

type slowSendPresentation struct {
	armed   atomic.Bool
	entered chan struct{}
	release chan struct{}
}

func (*slowSendPresentation) Available() bool { return true }

func (*slowSendPresentation) Ask(humanask.Message) (humanask.Answer, error) {
	return humanask.Answer{}, nil
}

func (n *slowSendPresentation) Presentation() notify.Presentation {
	if n.armed.CompareAndSwap(true, false) {
		close(n.entered)
		<-n.release
	}
	return notify.Presentation{}
}

func TestDeduplicatedSendKeepsOriginalReceiptAtBudgetThroughMCP(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		t.Run(version, func(t *testing.T) {
			n := &slowSendPresentation{entered: make(chan struct{}), release: make(chan struct{})}
			srv, journal, _ := sendReceiptServer(t, t.TempDir(), func(e *engine.Engine) { e.SetHumanNotifier(n) })
			var unblock sync.Once
			release := func() { unblock.Do(func() { close(n.release) }) }
			t.Cleanup(release)
			sender := sendReceiptSetup(t, srv, version)
			args := map[string]any{"token": sender, "to": "human", "type": "question", "body": "same identified request", "op_id": "bounded-dedup"}
			first := sendReceiptHTTP(context.Background(), srv, version, args)
			serial, ok := first.payload["msg_serial"].(float64)
			if first.err != nil || first.isError || !ok || first.payload["human_route"] != "desktop" {
				t.Fatalf("setup: initial identified desktop send failed: %+v", first)
			}
			before := journal.appends.Load()
			n.armed.Store(true)
			ctx, cancel := context.WithTimeout(context.Background(), 6500*time.Millisecond)
			defer cancel()
			answer := make(chan sendReceiptReply, 1)
			go func() { answer <- sendReceiptHTTP(ctx, srv, version, args) }()
			select {
			case <-n.entered:
			case reply := <-answer:
				t.Fatalf("setup: retry never reached the production presentation port: %+v", reply)
			case <-time.After(3 * time.Second):
				t.Fatal("setup: retry presentation was never reached")
			}
			reply := <-answer
			assertSendReceiptRetry(t, reply, uint64(serial))
			if reply.payload["advisories"] == nil || journal.appends.Load() != before {
				t.Fatalf("bounded retry lost its advisory boundary or appended again: %v", reply.payload)
			}
			release()
			assertSendReceiptRetry(t, sendReceiptHTTP(context.Background(), srv, version, args), uint64(serial))
			if journal.appends.Load() != before {
				t.Fatal("completed identified retry appended another operation")
			}
		})
	}
}

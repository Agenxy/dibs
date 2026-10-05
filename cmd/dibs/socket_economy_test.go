package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/ledger"
	"github.com/agenxy/dibs/internal/mcp"
)

// The actual stdio serving loop registers, claims the one-writer route and
// receives the real daemon's subscription. No eligibility setter is called.
func TestSocketEconomyBridgeHelper(t *testing.T) {
	if os.Getenv("DIBS_TEST_SOCKET_ECONOMY_BRIDGE") != "1" {
		return
	}
	if err := runBridge(nil); err != nil {
		t.Fatal(err)
	}
}

type economyResponse struct {
	http.ResponseWriter
	buf bytes.Buffer
}

func (w *economyResponse) Write(b []byte) (int, error) {
	_, _ = w.buf.Write(b)
	return w.ResponseWriter.Write(b)
}

func (w *economyResponse) Flush() { w.ResponseWriter.(http.Flusher).Flush() }

type economyFixture struct {
	srv     *httptest.Server
	eng     *engine.Engine
	lines   <-chan string
	offers  <-chan string
	settles <-chan bool
	in      io.Writer
	out     *bufio.Reader
	sid     string
	socket  string
	worker  string
	sender  string
}

func newEconomyFixture(t *testing.T) *economyFixture {
	return newEconomyFixtureOn(t, "71f97290-0001-4000-8000-111111111111")
}

func newEconomyFixtureOn(t *testing.T, sid string) *economyFixture {
	t.Helper()
	sock, dir := sockPath(t), t.TempDir()
	f := &economyFixture{sid: sid, socket: sock}
	f.lines = listenLines(t, sock)
	box, err := ledger.LoadOrCreateKey(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	led, err := ledger.Open(filepath.Join(dir, "ledger.jsonl"), "economy-host", box)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	f.eng = engine.New(core.NewState("economy-host", core.DefaultLimits()), led, nil)
	joined := make(chan struct{})
	go func() { f.eng.Run(ctx); close(joined) }()
	server := mcp.New(f.eng)
	offers := make(chan string, 16)
	settles := make(chan bool, 16)
	f.offers = offers
	f.settles = settles
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, readErr := io.ReadAll(r.Body)
		if readErr != nil {
			http.Error(w, readErr.Error(), http.StatusBadRequest)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(b))
		var req struct {
			ID     string `json:"id"`
			Method string `json:"method"`
			Params struct {
				Meta map[string]any `json:"_meta"`
			} `json:"params"`
		}
		_ = json.Unmarshal(b, &req)
		if req.Method != "resources/read" || req.Params.Meta[mcp.SocketOfferMetaKey] != true || req.ID != "dibs-wake-refresh" {
			server.ServeHTTP(w, r)
			return
		}
		if req.Params.Meta[mcp.SocketOfferIDMetaKey] != nil {
			server.ServeHTTP(w, r)
			written, _ := req.Params.Meta[mcp.SocketWrittenMetaKey].(bool)
			settles <- written
			return
		}
		out := &economyResponse{ResponseWriter: w}
		server.ServeHTTP(out, r)
		var reply struct {
			Result struct {
				Contents []struct{ Text string } `json:"contents"`
			} `json:"result"`
		}
		if json.Unmarshal(out.buf.Bytes(), &reply) == nil && len(reply.Result.Contents) == 1 {
			offers <- reply.Result.Contents[0].Text
		}
	}))
	t.Cleanup(func() { f.srv.Close(); cancel(); <-joined; _ = led.Close() })
	if err := os.WriteFile(filepath.Join(dir, "local.secret"), bytes.Repeat([]byte("a"), 64), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestSocketEconomyBridgeHelper$")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"DIBS_TEST_SOCKET_ECONOMY_BRIDGE=1", "DIBS_DIR="+dir,
		"DIBS_ADDR="+strings.TrimPrefix(f.srv.URL, "http://"), "DIBS_HOST_ID=economy-host",
		"DIBS_HARNESS=Claude Code", "DIBS_NOTIFY=off", "DIBS_AGENT_NONCE=",
		"CLAUDE_CODE_MESSAGING_SOCKET="+sock, "CLAUDE_CODE_MESSAGING_TOKEN=fixture-child-token")
	cmd.Stderr = os.Stderr
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	f.in, f.out = in, bufio.NewReader(out)
	var stop sync.Once
	t.Cleanup(func() { stop.Do(func() { _ = in.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() }) })
	f.worker = f.stdio(t, "register", map[string]any{"name": "worker", "session_id": f.sid})["token"].(string)
	// Registration starts the stream asynchronously. Observe its actual route
	// claim, rather than assuming the callback finished before the reply landed.
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	deadline := time.After(3 * time.Second)
	for {
		if live, _ := f.eng.SelfWaking("worker"); live {
			break
		}
		select {
		case <-ticker.C:
		case <-deadline:
			t.Fatal("setup: real bridge never claimed the socket route")
		}
	}
	f.sender = f.tool(t, "register", map[string]any{"name": "sender"})["token"].(string)
	return f
}

func (f *economyFixture) stdio(t *testing.T, name string, args map[string]any) map[string]any {
	t.Helper()
	req := map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{
			"name": name, "arguments": args,
			"_meta": map[string]any{mcp.SessionMetaKey: f.sid, "threadId": f.sid},
		},
	}
	if err := json.NewEncoder(f.in).Encode(req); err != nil {
		t.Fatal(err)
	}
	reply := make(chan []byte, 1)
	go func() { b, _ := f.out.ReadBytes('\n'); reply <- b }()
	select {
	case b := <-reply:
		return economyToolResult(t, b)
	case <-time.After(5 * time.Second):
		t.Fatal("setup: actual stdio tool did not reply")
		return nil
	}
}

func (f *economyFixture) offerRead(t *testing.T, extra map[string]any) (string, string) {
	t.Helper()
	meta := map[string]any{"com.dibs/token": f.worker, mcp.SessionMetaKey: f.sid, mcp.SocketOfferMetaKey: true}
	for k, v := range extra {
		meta[k] = v
	}
	b, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": "economy-test-read", "method": "resources/read",
		"params": map[string]any{"uri": mcp.WakeDigestURI, "_meta": meta},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := f.srv.Client().Post(f.srv.URL, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		Result struct {
			Contents []struct{ Text string } `json:"contents"`
			Meta     map[string]any          `json:"_meta"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || len(out.Result.Contents) != 1 {
		t.Fatalf("setup: real resource read failed: %v", err)
	}
	id, _ := out.Result.Meta[mcp.SocketOfferIDMetaKey].(string)
	return out.Result.Contents[0].Text, id
}

func (f *economyFixture) waitSettlement(t *testing.T) bool {
	t.Helper()
	select {
	case got := <-f.settles:
		return got
	case <-time.After(3 * time.Second):
		t.Fatal("actual bridge did not settle its write")
		return false
	}
}

func (f *economyFixture) waitWritten(t *testing.T) {
	t.Helper()
	if !f.waitSettlement(t) {
		t.Fatal("actual bridge reported a failed write")
	}
}

func (f *economyFixture) idle(t *testing.T) {
	t.Helper()
	f.tool(t, "hook_poll", map[string]any{"session_id": f.sid, "event": "Stop", "stop_hook_active": true})
}

func economyToolResult(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var reply struct {
		Result struct {
			Content []struct{ Text string } `json:"content"`
			IsError bool                    `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(b, &reply); err != nil || reply.Result.IsError || len(reply.Result.Content) != 1 {
		t.Fatalf("setup: MCP tool did not succeed: %s (%v)", b, err)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(reply.Result.Content[0].Text), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func (f *economyFixture) tool(t *testing.T, name string, args map[string]any) map[string]any {
	t.Helper()
	return f.toolOnHost(t, name, args, "economy-host")
}

func (f *economyFixture) toolOnHost(t *testing.T, name string, args map[string]any, host string) map[string]any {
	t.Helper()
	request := map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "tools/call",
		"params": map[string]any{"name": name, "arguments": args, "_meta": map[string]any{mcp.HostMetaKey: host}},
	}
	b, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := f.srv.Client().Post(f.srv.URL, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err = io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return economyToolResult(t, b)
}

func (f *economyFixture) hook(t *testing.T, event string) string {
	t.Helper()
	r := f.tool(t, "hook_poll", map[string]any{"session_id": f.sid, "event": event})
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func (f *economyFixture) informationalStop(t *testing.T) {
	t.Helper()
	r := f.tool(t, "hook_poll", map[string]any{"session_id": f.sid, "event": "Stop"})
	if r["decision"] == "block" || r["reason"] != nil || r["hookSpecificOutput"] != nil {
		t.Fatalf("informational-only Stop forced a model turn: %v", r)
	}
}

func (f *economyFixture) waitOffer(t *testing.T) string {
	t.Helper()
	select {
	case text := <-f.offers:
		return text
	case <-time.After(3 * time.Second):
		t.Fatal("setup: bridge did not reach the real socket-offer endpoint")
		return ""
	}
}

func TestSocketEconomyUsesLifecycleAndPreservesHookDelivery(t *testing.T) {
	for _, mode := range []string{"mid-turn-question", "mid-turn-notify", "idle-question", "idle-notify"} {
		t.Run(mode, func(t *testing.T) {
			f := newEconomyFixture(t)
			f.hook(t, "Stop")
			busy := strings.HasPrefix(mode, "mid-turn-")
			if busy {
				f.hook(t, "UserPromptSubmit")
			}
			kind := "question"
			if strings.HasSuffix(mode, "notify") {
				kind = "notify"
			}
			marker := "socket-economy-" + mode
			f.tool(t, "send", map[string]any{"token": f.sender, "to": "worker", "type": kind, "body": marker})
			text := f.waitOffer(t)
			if !busy {
				if !strings.Contains(text, marker) {
					t.Fatalf("idle actionable offer lost mail: %q", text)
				}
				wire := collect(f.lines, 2, time.Second)
				if len(wire) != 2 || !strings.Contains(strings.Join(wire, "\n"), marker) {
					t.Fatalf("idle actionable mail produced no actual socket frame: %v", wire)
				}
				f.waitWritten(t)
			} else {
				if text != "" {
					t.Errorf("%s mail offered a socket wake: %q", mode, text)
				}
				if wire := collect(f.lines, 1, 100*time.Millisecond); len(wire) != 0 {
					t.Errorf("%s mail wrote to the actual session socket: %v", mode, wire)
				}
			}
			got := f.tool(t, "hook_poll", map[string]any{"session_id": f.sid, "event": "Stop", "strict_output": true})
			if got["decision"] != "block" || !strings.Contains(fmt.Sprint(got), marker) {
				t.Fatalf("authored mail lost its blocking Stop fallback: %v", got)
			}
			if wire := collect(f.lines, 1, 1100*time.Millisecond); len(wire) != 0 {
				t.Fatalf("Stop delivery wrote an extra native frame: %v", wire)
			}
		})
	}
}

func TestSocketEconomyBridgeHonorsConfiguredPhase(t *testing.T) {
	for _, phase := range []string{"all", "urgent", "none"} {
		for _, kind := range []string{"notify", "question"} {
			t.Run(phase+"/"+kind, func(t *testing.T) {
				f := newEconomyFixture(t)
				f.eng.ApplySetting("wake.extend_turn_for", phase)
				// A configured command must not hide the bridge's real route
				// in the send result. The bridge claimed through registration.
				f.eng.SetWakeCommands(map[string]engine.WakeCommand{
					"claude code": {Argv: []string{os.Args[0], "-test.run=^TestSocketEconomyBridgeHelper$"}, Cooldown: time.Minute},
				})
				f.idle(t)
				marker := "bridge-phase-" + phase + "-" + kind
				sent := f.tool(t, "send", map[string]any{"token": f.sender, "to": "worker", "type": kind, "body": marker})
				text := f.waitOffer(t)
				wakes := phase != "none" && (phase == "all" || kind == "question")
				if wakes {
					if !strings.Contains(fmt.Sprint(sent["note"]), "bridge owns the socket route") {
						t.Fatalf("configured command hid the claiming bridge's actual send route: %v", sent)
					}
					if !strings.Contains(text, marker) {
						t.Fatalf("enabled phase lost authored bridge mail: %q", text)
					}
					wire := collect(f.lines, 2, time.Second)
					if len(wire) != 2 || !strings.Contains(strings.Join(wire, "\n"), marker) {
						t.Fatalf("enabled phase had no actual bridge frame: %v", wire)
					}
					f.waitWritten(t)
					got := f.tool(t, "hook_poll", map[string]any{"session_id": f.sid, "event": "Stop", "strict_output": true})
					if got["decision"] != "block" || !strings.Contains(fmt.Sprint(got), marker) {
						t.Fatalf("enabled bridge phase lost Stop fallback: %v", got)
					}
				} else {
					if text != "" {
						t.Fatalf("suppressed phase offered a wake: %q", text)
					}
					f.informationalStop(t)
					if got := f.hook(t, "SessionStart"); !strings.Contains(got, marker) {
						t.Fatalf("suppressed mail disappeared at natural activation: %s", got)
					}
				}
				if wire := collect(f.lines, 1, 1100*time.Millisecond); len(wire) != 0 {
					t.Fatalf("suppressed or already-delivered phase wrote a frame: %v", wire)
				}
			})
		}
	}
}

func TestSocketEconomyVerdictsUseCurrentWaitingAndCanonicalKind(t *testing.T) {
	for _, mode := range []string{"approved", "grant-approved", "answered", "flag", "flag-after-done", "progress", "accepted", "done-waiting", "done-unblocked"} {
		t.Run(mode, func(t *testing.T) {
			f := newEconomyFixture(t)
			f.tool(t, "check_in", map[string]any{"token": f.worker})
			review := mode == "flag" || mode == "flag-after-done" || mode == "accepted"
			from, to := f.worker, "sender"
			if review {
				from, to = f.sender, "worker"
			}
			kind := "request"
			approver := f.sender
			if mode == "answered" {
				kind = "question"
			}
			args := map[string]any{"token": from, "to": to, "type": kind, "body": "verdict fixture"}
			if kind == "request" {
				args["milestones"] = []string{"build"}
			}
			if mode == "grant-approved" {
				human, token, err := f.eng.HumanAgent(t.Context())
				if err != nil || human == "" || token == "" {
					t.Fatalf("setup: human approval identity: %v", err)
				}
				args["to"], args["grant"] = "human", "coordinator"
				delete(args, "milestones")
				approver = token // the OS-owned actor must approve its own request
			}
			sent := f.tool(t, "send", args)
			serial := sent["msg_serial"]
			if review {
				// The setup request is deliberately mid-turn, so it must not
				// spend the socket before the review under test exists.
				f.hook(t, "UserPromptSubmit")
				f.waitOffer(t)
			}
			worker := approver
			if review {
				worker = f.worker
			}
			respond := func(disposition string, milestone bool) {
				a := map[string]any{"token": worker, "msg_serial": serial, "disposition": disposition, "body": "fixture update"}
				if milestone {
					a["milestone"] = 1
				}
				f.tool(t, "respond", a)
			}
			if mode != "approved" && mode != "grant-approved" && mode != "answered" {
				respond("approve", false)
				if !review {
					if got := f.waitOffer(t); got != "" {
						t.Fatalf("setup: busy approval offered a wake: %q", got)
					}
					f.tool(t, "read_mail", map[string]any{"token": f.worker, "msg_serial": serial})
				}
			}
			if review {
				respond("progress", true)
				if mode == "flag-after-done" {
					respond("done", false)
				}
				worker = f.sender // review belongs to the request's sender
			}
			if mode == "done-waiting" {
				f.tool(t, "declare", map[string]any{"token": f.worker, "text": "waiting for result", "waiting": "sender"})
			}
			f.idle(t)
			switch mode {
			case "approved", "grant-approved":
				respond("approve", false)
			case "answered":
				respond("answer", false)
			case "flag", "flag-after-done":
				respond("flag", true)
			case "accepted":
				respond("accept", true)
			case "progress":
				respond("progress", true)
			default:
				respond("done", false)
			}
			text := f.waitOffer(t)
			wakes := mode == "grant-approved" || mode == "answered" || mode == "flag" || mode == "flag-after-done" || mode == "done-waiting"
			if wakes {
				if text == "" || len(collect(f.lines, 2, time.Second)) != 2 {
					t.Fatalf("idle %s produced no real socket wake: %q", mode, text)
				}
				f.waitWritten(t)
			} else {
				wire := collect(f.lines, 2, 100*time.Millisecond)
				if text != "" || len(wire) != 0 {
					t.Fatalf("informational %s offered %q and wrote frames %v", mode, text, wire)
				}
			}
			event := "Stop"
			if !wakes {
				f.informationalStop(t)
				event = "SessionStart"
			}
			if got := f.hook(t, event); !strings.Contains(got, "AGENT:") {
				t.Fatalf("%s lost its full hook fallback: %s", mode, got)
			}
		})
	}
}

func TestSocketEconomyDueWaitsShareOneWriterAndRetainBusyFallback(t *testing.T) {
	for _, busy := range []bool{false, true} {
		t.Run(map[bool]string{false: "idle", true: "busy"}[busy], func(t *testing.T) {
			f := newEconomyFixture(t)
			f.tool(t, "check_in", map[string]any{"token": f.worker})
			for _, item := range []struct{ text, after string }{{"due-one", "1s"}, {"due-two", "1s"}, {"later-wait", "1h"}} {
				f.tool(t, "declare", map[string]any{"token": f.worker, "text": item.text, "waiting": "ci", "recheck_after": item.after})
			}
			f.idle(t)
			if busy {
				f.hook(t, "UserPromptSubmit")
			}
			before, err := f.eng.Board(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if busy {
				if wire := collect(f.lines, 1, 2500*time.Millisecond); len(wire) != 0 {
					t.Fatalf("a due wait interrupted a busy turn: %v", wire)
				}
				got := f.hook(t, "Stop")
				if !strings.Contains(got, "due-one") || !strings.Contains(got, "due-two") || strings.Contains(got, "later-wait") {
					t.Fatalf("Stop did not carry exactly the due slots: %s", got)
				}
				f.idle(t)
			}
			text := f.waitOffer(t)
			if !strings.Contains(text, "due-one") || !strings.Contains(text, "due-two") || strings.Contains(text, "later-wait") {
				t.Fatalf("one offer did not carry exactly the due slots: %q", text)
			}
			if len(collect(f.lines, 2, time.Second)) != 2 {
				t.Fatal("due waits had no actual socket delivery")
			}
			f.waitWritten(t)
			if extra := collect(f.lines, 1, 1200*time.Millisecond); len(extra) != 0 {
				t.Fatalf("due slots produced duplicate socket writes: %v", extra)
			}
			after, err := f.eng.Board(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if before["serial"] != after["serial"] {
				t.Fatal("derived readiness changed the ledger serial")
			}
		})
	}
}

func TestSocketEconomyUnknownBusyAndCoalescingBeyondCooldown(t *testing.T) {
	for _, mode := range []string{"unknown", "busy", "coalesced"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			f := newEconomyFixture(t)
			if mode != "unknown" {
				f.idle(t)
			}
			if mode == "busy" {
				f.hook(t, "UserPromptSubmit")
			}
			f.tool(t, "send", map[string]any{"token": f.sender, "to": "worker", "type": "question", "body": "long-window-first"})
			initial := f.waitOffer(t)
			if mode == "coalesced" {
				if initial == "" || len(collect(f.lines, 2, time.Second)) != 2 {
					t.Fatal("setup: first idle wake missing")
				}
				f.waitWritten(t)
			} else if initial != "" {
				t.Fatalf("%s ignored bounded grace or explicit busy: %q", mode, initial)
			}
			if mode == "unknown" {
				if len(collect(f.lines, 2, 25*time.Second)) != 2 {
					t.Fatal("unknown state stranded actionable mail beyond its existing grace")
				}
				f.waitWritten(t)
			} else {
				if wire := collect(f.lines, 1, 21*time.Second); len(wire) != 0 {
					t.Fatalf("%s duplicated or aged into idle: %v", mode, wire)
				}
			}
			f.tool(t, "send", map[string]any{"token": f.sender, "to": "worker", "type": "question", "body": "long-window-second"})
			if text, _ := f.offerRead(t, nil); text != "" {
				t.Fatalf("%s rearmed from time or a new message serial: %q", mode, text)
			}
			if got := f.hook(t, "Stop"); !strings.Contains(got, "long-window-first") || !strings.Contains(got, "long-window-second") {
				t.Fatalf("coalescing lost pending mail at Stop: %s", got)
			}
		})
	}
}

func TestSocketEconomyBatchesOwnedMailboxesAndRearmerUsesReceipt(t *testing.T) {
	f := newEconomyFixtureOn(t, "shared-economy-session")
	other := f.stdio(t, "register", map[string]any{"name": "worker-alt", "session_id": f.sid})
	alt, ok := other["token"].(string)
	if !ok || alt == f.worker || other["agent_id"] == "worker" {
		t.Fatal("setup: registration did not create a distinct mailbox")
	}
	deadline := time.After(3 * time.Second)
	for {
		if live, _ := f.eng.SelfWaking("worker-alt"); live {
			break
		}
		select {
		case <-time.After(time.Millisecond):
		case <-deadline:
			t.Fatal("setup: actual bridge did not claim its second mailbox")
		}
	}
	f.idle(t)
	f.hook(t, "UserPromptSubmit")
	f.tool(t, "send", map[string]any{"token": f.sender, "to": "worker", "type": "notify", "body": "owned-fyi"})
	if got := f.waitOffer(t); got != "" {
		t.Fatalf("authored notify interrupted the shared busy session: %q", got)
	}
	f.tool(t, "send", map[string]any{"token": f.sender, "to": "worker-alt", "type": "question", "body": "owned-question"})
	if got := f.waitOffer(t); got != "" {
		t.Fatalf("question interrupted the shared busy session: %q", got)
	}
	f.idle(t)
	got := f.waitOffer(t)
	if !strings.Contains(got, "owned-fyi") || !strings.Contains(got, "owned-question") {
		t.Fatalf("atomic owned batch lost a mailbox: %q", got)
	}
	if len(collect(f.lines, 2, time.Second)) != 2 {
		t.Fatal("batch had no actual socket write")
	}
	f.waitWritten(t)
	if text, _ := f.offerRead(t, map[string]any{"com.dibs/token": alt}); text != "" {
		t.Fatalf("second mailbox bypassed the session epoch: %q", text)
	}
	// A different fresh session/host cannot be aggregated by a matching id.
	foreign := f.tool(t, "register", map[string]any{"name": "foreign", "session_id": "foreign-session"})["token"].(string)
	f.tool(t, "send", map[string]any{"token": f.sender, "to": "foreign", "type": "question", "body": "foreign-private"})
	otherHost := f.toolOnHost(t, "register", map[string]any{"name": "other-host", "session_id": f.sid}, "other-host-id")["token"].(string)
	f.tool(t, "send", map[string]any{"token": f.sender, "to": "other-host", "type": "question", "body": "other-host-private"})
	f.hook(t, "UserPromptSubmit")
	if text, _ := f.offerRead(t, map[string]any{mcp.SocketTokensMetaKey: []string{f.worker, alt}}); text != "" {
		t.Fatalf("turn evidence did not confirm the entire accepted batch: %q", text)
	}
	// New mail rearms after the shared turn. Authentication of every token
	// still excludes foreign-session text from the new atomic reservation.
	f.tool(t, "send", map[string]any{"token": f.sender, "to": "worker-alt", "type": "question", "body": "retry-question"})
	if text, _ := f.offerRead(t, map[string]any{mcp.SocketTokensMetaKey: []string{f.worker, alt}}); text != "" {
		t.Fatalf("new mail interrupted the shared busy turn: %q", text)
	}
	f.idle(t)
	text, id := f.offerRead(t, map[string]any{mcp.SocketTokensMetaKey: []string{f.worker, alt, foreign, otherHost}})
	if id == "" || !strings.Contains(text, "retry-question") || strings.Contains(text, "foreign-private") || strings.Contains(text, "other-host-private") {
		t.Fatalf("batch binding/authentication failed: %q", text)
	}
	f.offerRead(t, map[string]any{mcp.SocketOfferIDMetaKey: id, mcp.SocketWrittenMetaKey: false})
	// The failure is reported through the production MCP resource; it releases
	// only its own reservation and spends no mail presentation or due-wait retry.
	text, retry := f.offerRead(t, map[string]any{mcp.SocketTokensMetaKey: []string{f.worker, alt}})
	if retry == "" || retry == id || !strings.Contains(text, "retry-question") {
		t.Fatalf("failed write did not rearm owned mail: %q", text)
	}
	f.offerRead(t, map[string]any{mcp.SocketOfferIDMetaKey: retry, mcp.SocketWrittenMetaKey: false})
}

func TestSocketEconomyFailedBridgeWriteRearmsWithoutConsuming(t *testing.T) {
	f := newEconomyFixture(t)
	f.idle(t)
	// The actual bridge still owns its route, but the receiver has disappeared
	// after discovery. ENOENT drives the real failed-write receipt and surrender.
	if err := os.Remove(f.socket); err != nil {
		t.Fatal("setup: remove the private receiver socket:", err)
	}
	f.tool(t, "send", map[string]any{"token": f.sender, "to": "worker", "type": "question", "body": "failed-write-mail"})
	if offer := f.waitOffer(t); !strings.Contains(offer, "failed-write-mail") {
		t.Fatalf("setup: actual bridge did not offer the question: %q", offer)
	}
	if f.waitSettlement(t) {
		t.Fatal("missing receiver was reported as written")
	}
	if lines := collect(f.lines, 1, 100*time.Millisecond); len(lines) != 0 {
		t.Fatalf("failed receiver somehow received a frame: %q", lines)
	}
	text, retry := f.offerRead(t, nil)
	if retry == "" || !strings.Contains(text, "failed-write-mail") {
		t.Fatalf("a real failed write spent presentation or wedged its reservation: %q", text)
	}
	f.offerRead(t, map[string]any{mcp.SocketOfferIDMetaKey: retry, mcp.SocketWrittenMetaKey: false})
	if text := f.hook(t, "Stop"); !strings.Contains(text, "failed-write-mail") {
		t.Fatalf("failed native write lost its real hook fallback: %q", text)
	}
}

func TestSocketEconomyDueRetriesRemainBoundedAndReportTheAssigner(t *testing.T) {
	f := newEconomyFixture(t)
	f.tool(t, "check_in", map[string]any{"token": f.worker})
	sent := f.tool(t, "send", map[string]any{"token": f.sender, "to": "worker", "type": "request", "body": "due-budget-work"})
	serial := sent["msg_serial"]
	f.tool(t, "respond", map[string]any{"token": f.worker, "msg_serial": serial, "disposition": "approve"})
	f.tool(t, "declare", map[string]any{
		"token": f.worker, "text": "due-budget-wait", "waiting": "ci",
		"recheck_after": "1s", "refs": []string{fmt.Sprintf("request:%.0f", serial)},
	})
	f.idle(t)
	for attempt := range 3 {
		lines := collect(f.lines, 2, 20*time.Second)
		if len(lines) != 2 || !strings.Contains(lines[1], "due-budget-wait") || !strings.Contains(lines[1], "due-budget-work") {
			t.Fatalf("due retry %d did not quote the real parked request and declaration: %q", attempt+1, lines)
		}
		f.waitWritten(t)
		f.hook(t, "UserPromptSubmit")
		f.idle(t)
	}
	deadline := time.Now().Add(17 * time.Second)
	for {
		board, err := f.eng.Board(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		stalled := false
		for _, row := range board["agents"].([]map[string]any) {
			if row["id"] == "worker" && row["work"] == "stalled" {
				stalled = true
			}
		}
		if stalled {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("bounded native rechecks never set the real stalled row")
		}
		<-time.After(50 * time.Millisecond)
	}
	if text, _ := f.offerRead(t, nil); text != "" {
		t.Fatalf("exhausted inherited/declared wait offered a fourth wake: %q", text)
	}
	deadline = time.Now().Add(3 * time.Second)
	for {
		r := f.tool(t, "inbox", map[string]any{"token": f.sender})
		b, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "worker has stalled") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("bounded native rechecks never told the actual request assigner")
		}
		<-time.After(50 * time.Millisecond)
	}
	if lines := collect(f.lines, 1, 1100*time.Millisecond); len(lines) != 0 {
		t.Fatalf("stalled wait still wrote to the native socket: %q", lines)
	}
}

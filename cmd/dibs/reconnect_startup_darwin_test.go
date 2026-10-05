package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/ledger"
	"github.com/agenxy/dibs/internal/mcp"
	"github.com/agenxy/dibs/internal/notify"
)

// ChatGPT.app's executable-path process inventory is a macOS capability.
// The fixture is a running harness, not an engine setter: two independent
// stdio bridges descend from one app process and speak to the real HTTP MCP
// handler. A replacement app must recover mail before any model tool call.
func TestAppReconnectRecoversMailBeforeAModelCall(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess bridge and queue contract")
	}
	for _, tc := range []struct {
		name            string
		lost, clockBack bool
		legacy          bool
	}{
		{"lost-queue-unavailable-observer", true, false, false},
		{"retained-native-queue", false, false, false},
		{"clock-moved-backward", true, true, false},
		{"legacy-initialize", true, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) { appReconnectContract(t, tc.lost, tc.clockBack, tc.legacy) })
	}
}

type reconnectBridge struct {
	cmd *exec.Cmd
	in  io.WriteCloser
	out *json.Decoder
}

// Copying this test executable under an app-shaped path gives ps the same
// ancestry production observes, without touching the person's real app.
func TestReconnectAppFixtureHelper(t *testing.T) {
	if os.Getenv("DIBS_TEST_RECONNECT_APP") != "1" {
		return
	}
	var bridges [2]reconnectBridge
	for i := range bridges {
		cmd := exec.Command(os.Getenv("DIBS_TEST_BRIDGE_BINARY"), "-test.run=^TestBridgeStartupHelper$")
		cmd.Env = append(os.Environ(), "DIBS_TEST_STARTUP_BRIDGE=1")
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
		bridges[i] = reconnectBridge{cmd: cmd, in: in, out: json.NewDecoder(out)}
		defer func() { _ = in.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	}
	decoder, encoder := json.NewDecoder(os.Stdin), json.NewEncoder(os.Stdout)
	for {
		var request struct {
			Bridge int             `json:"bridge"`
			RPC    json.RawMessage `json:"rpc"`
		}
		if err := decoder.Decode(&request); err == io.EOF {
			return
		} else if err != nil {
			t.Fatal(err)
		}
		if request.Bridge < 0 || request.Bridge >= len(bridges) {
			t.Fatal("invalid bridge fixture index")
		}
		bridge := &bridges[request.Bridge]
		if _, err := bridge.in.Write(append(request.RPC, '\n')); err != nil {
			t.Fatal(err)
		}
		var reply json.RawMessage
		if err := bridge.out.Decode(&reply); err != nil {
			t.Fatal(err)
		}
		if err := encoder.Encode(reply); err != nil {
			t.Fatal(err)
		}
	}
}

func appReconnectContract(t *testing.T, lost, clockBack, legacy bool) {
	t.Helper()
	if _, err := notify.DesktopState(); err == nil {
		t.Fatal("fixture must not resolve a native desktop helper")
	}
	home, dir := t.TempDir(), t.TempDir()
	t.Setenv("CODEX_HOME", home)
	t.Setenv("DIBS_DIR", dir)
	t.Setenv("DIBS_HOST_ID", "reconnect-host")
	t.Setenv("DIBS_NOTIFY", "off")
	queueBinary := filepath.Join(t.TempDir(), "codex")
	if out, err := exec.Command("go", "build", "-o", queueBinary, "../../internal/wakeexec/testdata/codexqueue").CombinedOutput(); err != nil {
		t.Fatalf("queue setup: %v %s", err, out)
	}
	limits := core.DefaultLimits()
	state := core.NewState("reconnect-host", limits)
	box, err := ledger.LoadOrCreateKey(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	led, err := ledger.Open(filepath.Join(dir, "ledger.jsonl"), "reconnect-host", box)
	if err != nil {
		t.Fatal(err)
	}
	eng := engine.New(state, led, nil)
	eng.SetWakeCommands(map[string]engine.WakeCommand{"codex": {Argv: []string{queueBinary, "queue", "--thread", "{thread}", "--message", "{message}"}, Cooldown: time.Millisecond}})
	ctx, cancel := context.WithCancel(context.Background())
	joined := make(chan struct{})
	go func() { eng.Run(ctx); close(joined) }()
	srv := httptest.NewServer(mcp.New(eng))
	t.Cleanup(func() { srv.Close(); cancel(); <-joined; _ = led.Close() })
	if err := os.WriteFile(filepath.Join(dir, "local.secret"), bytes.Repeat([]byte("a"), 64), 0o600); err != nil {
		t.Fatal(err)
	}
	appBinary := filepath.Join(t.TempDir(), "ChatGPT.app", "Contents", "MacOS", "ChatGPT")
	if err := os.MkdirAll(filepath.Dir(appBinary), 0o700); err != nil {
		t.Fatal(err)
	}
	self, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(appBinary, self, 0o700); err != nil {
		t.Fatal(err)
	}
	startApp := func() (func(int), func()) {
		cmd := exec.Command(appBinary, "-test.run=^TestReconnectAppFixtureHelper$")
		cmd.Env = append(os.Environ(), "DIBS_TEST_RECONNECT_APP=1", "DIBS_TEST_BRIDGE_BINARY="+os.Args[0], "DIBS_ADDR="+srv.URL+"/mcp")
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		in, er := cmd.StdinPipe()
		if er != nil {
			t.Fatal(er)
		}
		out, er := cmd.StdoutPipe()
		if er != nil {
			t.Fatal(er)
		}
		if er = cmd.Start(); er != nil {
			t.Fatal(er)
		}
		reader := bufio.NewReader(out)
		call := func(bridge int) {
			t.Helper()
			method, params := "server/discover", map[string]any{"_meta": map[string]any{"protocolVersion": "2026-07-28"}}
			if legacy {
				method, params = "initialize", map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "reconnect-fixture", "version": "1"}}
			}
			request := map[string]any{"bridge": bridge, "rpc": map[string]any{"jsonrpc": "2.0", "id": bridge + 1, "method": method, "params": params}}
			if er := json.NewEncoder(in).Encode(request); er != nil {
				t.Fatal(er)
			}
			replies := make(chan []byte, 1)
			go func() { line, _ := reader.ReadBytes('\n'); replies <- line }()
			select {
			case line := <-replies:
				var reply map[string]any
				if er := json.Unmarshal(line, &reply); er != nil || reply["result"] == nil {
					t.Fatalf("startup: %s (%v)", line, er)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("bridge startup timed out")
			}
		}
		stop := func() { _ = in.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() }
		t.Cleanup(stop)
		return call, stop
	}
	oldStartup, oldStop := startApp()
	oldStartup(0) // establish the old app cohort before mail exists
	do := func(op *core.Op) core.Result {
		t.Helper()
		r, er := eng.Do(ctx, op)
		if er != nil {
			t.Fatal(er)
		}
		return r
	}
	thread := "01a0aaaa-bbbb-7ccc-8ddd-eeeeeeeeeeee"
	worker := do(&core.Op{Kind: core.OpRegister, Name: "worker", Description: "app-owned worker", AgentKind: core.KindPersistent, Nonce: "worker-nonce", SessionID: thread, Agent: &core.AgentInfo{Harness: "Codex", Surface: "chatgpt-app", HostID: "reconnect-host", CWD: home}})
	asker := do(&core.Op{Kind: core.OpRegister, Name: "asker", Description: "sender", Nonce: "asker-nonce"})
	var fyiToken string
	for _, row := range []struct{ id, host string }{{"empty", "reconnect-host"}, {"other-host", "another-computer"}, {"fyi-only", "reconnect-host"}} {
		sid := strings.Replace(thread, "aaaa", map[string]string{"empty": "1111", "other-host": "2222", "fyi-only": "3333"}[row.id], 1)
		registered := do(&core.Op{Kind: core.OpRegister, Name: row.id, Description: "cohort exclusion fixture", AgentKind: core.KindPersistent, Nonce: row.id + "-nonce", SessionID: sid, Agent: &core.AgentInfo{Harness: "Codex", Surface: "chatgpt-app", HostID: row.host, CWD: home}})
		if row.id == "fyi-only" {
			fyiToken = registered["token"].(string)
		}
	}
	do(&core.Op{Kind: core.OpAckBoard, Token: asker["token"].(string)})
	do(&core.Op{Kind: core.OpSendMessage, Token: asker["token"].(string), To: "other-host", MsgType: core.MsgQuestion, Body: "another computer's mail"})
	do(&core.Op{Kind: core.OpSendMessage, Token: asker["token"].(string), To: worker["agent_id"].(string), MsgType: core.MsgQuestion, Body: "mail must survive app restart", OpID: "receipt-check"})
	waitQueueCount(t, home, 1)
	fyi := do(&core.Op{Kind: core.OpSendMessage, Token: asker["token"].(string), To: "fyi-only", MsgType: core.MsgNotify, Body: "authored FYI must reach its recipient"})
	waitQueueCount(t, home, 2)
	// Consuming an app queue entry is not an agent reading it. Present the
	// authored FYI through a real check-in, deliberately leave it unacked,
	// and prove two later idle epochs and app reconnect do not wake it again.
	presented := do(&core.Op{Kind: core.OpAckBoard, Token: fyiToken})
	mail, ok := presented["inbox"].([]*core.Message)
	if !ok || len(mail) != 1 || mail[0].Serial != fyi["msg_serial"].(uint64) ||
		mail[0].State != core.MsgStateDelivered || mail[0].Consumed {
		t.Fatal("setup: authored FYI was not presented once and left unacked")
	}
	fyiSession := strings.Replace(thread, "aaaa", "3333", 1)
	for range 2 {
		if _, er := eng.HookPoll(ctx, fyiSession, "PreToolUse", "", false, true); er != nil {
			t.Fatal("setup: next FYI turn:", er)
		}
		got, er := eng.HookPoll(ctx, fyiSession, "Stop", "", false, true)
		if er != nil || got["decision"] == "block" || got["reason"] != nil || got["hookSpecificOutput"] != nil {
			t.Fatalf("presented unacked FYI blocked a later Stop: %v %v", got, er)
		}
	}
	queued, er := os.ReadFile(filepath.Join(home, "pending.json"))
	if er != nil {
		t.Fatal(er)
	}
	var rows []map[string]any
	if er = json.Unmarshal(queued, &rows); er != nil {
		t.Fatal(er)
	}
	retained := []map[string]any{}
	for _, row := range rows {
		if row["threadId"] == thread {
			retained = append(retained, row)
		}
	}
	queued, er = json.Marshal(retained)
	if er != nil {
		t.Fatal(er)
	}
	if er = os.WriteFile(filepath.Join(home, "pending.json"), queued, 0o600); er != nil {
		t.Fatal(er)
	}
	oldStop()
	if clockBack {
		// The earlier queue receipt was written before the clock moved back.
		// Reconnect must invalidate it regardless of wall-clock ordering.
		key := sha256.Sum256([]byte(thread))
		path := filepath.Join(dir, "queued-wakes", hex.EncodeToString(key[:])+".json")
		waitFutureQueueReceipt(t, path)
	}
	if lost {
		if er := os.WriteFile(filepath.Join(home, "pending.json"), []byte("[]"), 0o600); er != nil {
			t.Fatal(er)
		}
		t.Setenv("DIBS_QUEUE_PROBE_FAIL", "1")
	}
	methodsBefore, _ := os.ReadFile(filepath.Join(home, "methods"))
	ledgerBefore, er := os.ReadFile(filepath.Join(dir, "ledger.jsonl"))
	if er != nil {
		t.Fatal(er)
	}
	// Queue commands settle asynchronously; a short quiet interval cannot
	// prove this row was excluded. Startup writes the real reconnect receipt
	// synchronously for each selected target before returning the RPC reply.
	fyiKey := sha256.Sum256([]byte(fyiSession))
	fyiReconnect := filepath.Join(dir, "queued-wakes", hex.EncodeToString(fyiKey[:])+".json.reconnect")
	if _, er = os.Stat(fyiReconnect); !os.IsNotExist(er) {
		t.Fatalf("setup: FYI already has a reconnect receipt: %v", er)
	}
	newStartup, _ := startApp()
	newStartup(0) // no token, register, check_in, or model turn
	workerKey := sha256.Sum256([]byte(thread))
	workerReconnect := filepath.Join(dir, "queued-wakes", hex.EncodeToString(workerKey[:])+".json.reconnect")
	if _, er = os.Stat(workerReconnect); er != nil {
		t.Fatalf("setup: actual app startup did not reconsider the unread worker: %v", er)
	}
	if _, er = os.Stat(fyiReconnect); !os.IsNotExist(er) {
		t.Fatalf("presented unacked FYI was incorrectly selected for app reconnect: %v", er)
	}
	waitQueueCount(t, home, 1)
	if !lost {
		deadline := time.Now().Add(2 * time.Second)
		for {
			methods, _ := os.ReadFile(filepath.Join(home, "methods"))
			if bytes.Count(methods, []byte("thread/queue/list")) > bytes.Count(methodsBefore, []byte("thread/queue/list")) {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("reconnect did not reconsider the retained queue/opening route")
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	newStartup(1) // another bridge in the same app is not another reconnect
	newStartup(0)
	time.Sleep(100 * time.Millisecond) // allow an erroneous second async queue command to settle
	if n := reconnectQueueCount(t, home); n != 1 {
		t.Fatalf("same app incarnation duplicated queued wake: %d", n)
	}
	ledgerAfter, er := os.ReadFile(filepath.Join(dir, "ledger.jsonl"))
	if er != nil || !bytes.Equal(ledgerBefore, ledgerAfter) {
		t.Fatalf("reconnect changed coordination state or identity: %v", er)
	}
	// The sender-facing receipt must describe the actual accepted queue route,
	// rather than inheriting the fold's "dormant, when it next wakes" note.
	deadline := time.Now().Add(time.Second)
	for {
		note := reconnectSendNote(t, srv, asker["token"].(string))
		if strings.Contains(note, "queued in the app") && !strings.Contains(note, "currently dormant") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("send did not report actual queue acceptance: %q", note)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func waitFutureQueueReceipt(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		b, err := os.ReadFile(path)
		var receipt map[string]any
		if err == nil && json.Unmarshal(b, &receipt) == nil {
			receipt["queued_at"] = time.Now().Add(time.Hour).UTC()
			b, err = json.Marshal(receipt)
			if err == nil {
				err = os.WriteFile(path, b, 0o600)
			}
			if err != nil {
				t.Fatal(err)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("initial queue receipt was not written")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func reconnectSendNote(t *testing.T, srv *httptest.Server, token string) string {
	t.Helper()
	request := map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "send", "arguments": map[string]any{"token": token, "to": "worker", "type": "question", "body": "mail must survive app restart", "op_id": "receipt-check"}}}
	b, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	response, err := srv.Client().Post(srv.URL, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	var reply struct {
		Result struct {
			Content []struct{ Text string } `json:"content"`
			IsError bool                    `json:"isError"`
		} `json:"result"`
	}
	if err = json.NewDecoder(response.Body).Decode(&reply); err != nil || reply.Result.IsError || len(reply.Result.Content) == 0 {
		t.Fatalf("send receipt setup failed: %+v %v", reply, err)
	}
	var result struct{ Note string }
	if err = json.Unmarshal([]byte(reply.Result.Content[0].Text), &result); err != nil {
		t.Fatal(err)
	}
	return result.Note
}

func reconnectQueueCount(t *testing.T, home string) int {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(home, "pending.json"))
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	var rows []json.RawMessage
	if err = json.Unmarshal(b, &rows); err != nil {
		t.Fatal(err)
	}
	return len(rows)
}

func waitQueueCount(t *testing.T, home string, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if reconnectQueueCount(t, home) == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("queue count=%d, want %d", reconnectQueueCount(t, home), want)
}

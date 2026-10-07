package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/ledger"
)

// Native replay, writer and first HTTP Accept are the production entry points.
// No Ready/activity setter or test-built index enters this fixture. The guard
// uses only pre-feature exported APIs, so old source reaches the MCP verdict.
func historyServer(t *testing.T, dir string) (*httptest.Server, func()) {
	t.Helper()
	box, err := ledger.LoadOrCreateKey(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal("setup:", err)
	}
	led, err := ledger.Open(filepath.Join(dir, "ledger.jsonl"), "history-mcp", box)
	if err != nil {
		t.Fatal("setup:", err)
	}
	st := core.NewState("history-mcp", core.DefaultLimits())
	if _, err := led.Replay(st); err != nil {
		t.Fatal("setup:", err)
	}
	eng := engine.New(st, led, nil)
	ctx, cancel := context.WithCancel(context.Background())
	joined := make(chan struct{})
	go func() { eng.Run(ctx); close(joined) }()
	if _, _, err := eng.SubscribeInfo(ctx, ""); err != nil {
		t.Fatal("setup:", err)
	}
	srv := httptest.NewUnstartedServer(New(eng))
	srv.Listener = led.MailHistory().ServingListener(ctx, srv.Listener)
	srv.Start()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			srv.Close()
			cancel()
			<-joined
			if err := led.Close(); err != nil {
				t.Error(err)
			}
		})
	}
	t.Cleanup(stop)
	return srv, stop
}

func historyCall(t *testing.T, srv *httptest.Server, version string, args map[string]any) map[string]any {
	t.Helper()
	payload, _ := historyExchange(t, srv, version, args)
	return payload
}

func historyExchange(t *testing.T, srv *httptest.Server, version string, args map[string]any) (map[string]any, map[string]any) {
	t.Helper()
	until := time.Now().Add(5 * time.Second)
	for {
		out := rpc(t, srv, version, "tools/call", map[string]any{"name": "mail_history", "arguments": args})
		result, ok := out["result"].(map[string]any)
		if !ok {
			t.Fatalf("history real MCP door failed: %v", out)
		}
		content := result["content"].([]any)[0].(map[string]any)["text"].(string)
		var payload map[string]any
		if err := json.Unmarshal([]byte(content), &payload); err != nil {
			t.Fatal(err)
		}
		if result["isError"] != true {
			return payload, out
		}
		if payload["code"] != "E_HISTORY_WARMING" && payload["code"] != "E_HISTORY_SETTLING" {
			t.Fatalf("history real MCP door failed: %v", payload)
		}
		if time.Now().After(until) {
			t.Fatalf("history real MCP door never settled: %v", payload)
		}
		// The public history API offers a retry hint rather than a readiness
		// subscription. This bounded client retry follows that real contract.
		<-time.NewTimer(10 * time.Millisecond).C
	}
}

func TestMailHistoryRealMCPMetadataAndContent(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		t.Run(version, func(t *testing.T) {
			dir := t.TempDir()
			srv, stop := historyServer(t, dir)
			_, sender := aliasRegister(t, srv, "history-sender", "history-sender-secret")
			_, recipient := aliasRegister(t, srv, "history-recipient", "history-recipient-secret")
			_, stranger := aliasRegister(t, srv, "history-stranger", "history-stranger-secret")
			sent := aliasCall(t, srv, "send", map[string]any{"token": sender, "to": "history-recipient", "type": "question", "body": "PRIVATE-HISTORY-CONTENT", "choices": []string{"PRIVATE-CHOICE"}})
			parent, ok := sent["msg_serial"].(float64)
			if !ok || parent == 0 {
				t.Fatal("setup: missing parent", sent)
			}
			for _, token := range []string{sender, recipient} {
				meta := historyCall(t, srv, version, map[string]any{"token": token, "limit": 1})
				rows, ok := meta["units"].([]any)
				if !ok || len(rows) != 1 || meta["as_of_serial"] == nil || meta["behind_by"] == nil {
					t.Fatalf("history metadata/boundary missing: %v", meta)
				}
				raw, _ := json.Marshal(meta)
				if strings.Contains(string(raw), "PRIVATE-") {
					t.Fatal("default history leaked authored content")
				}
				full := historyCall(t, srv, version, map[string]any{"token": token, "include_bodies": true, "limit": 1})
				raw, _ = json.Marshal(full)
				if !strings.Contains(string(raw), "PRIVATE-HISTORY-CONTENT") || !strings.Contains(string(raw), "PRIVATE-CHOICE") {
					t.Fatalf("authorized conversation text missing: %s", raw)
				}
				unit := full["units"].([]any)[0].(map[string]any)["unit"].(map[string]any)
				if unit["metadata"].(map[string]any)["state"] != "pending" {
					t.Fatalf("history consumed pending mail: %v", unit)
				}
			}
			other := historyCall(t, srv, version, map[string]any{"token": stranger, "include_bodies": true})
			if len(other["units"].([]any)) != 0 {
				t.Fatalf("stranger got history: %v", other)
			}
			stop()
			snapshot := historyReplayed(t, dir)
			m := snapshot.Messages[uint64(parent)]
			if m == nil || m.State != core.MsgStatePending || m.Consumed || m.DeliveredAt != 0 ||
				m.OutcomeReadAt != 0 || m.ReviewReadAt != 0 || m.AckedAt != 0 {
				t.Fatalf("history changed replayable live read markers: %+v", m)
			}
		})
	}
}

func historyReplayed(t *testing.T, dir string) *core.State {
	t.Helper()
	box, err := ledger.LoadOrCreateKey(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal("setup:", err)
	}
	led, err := ledger.OpenReadOnly(filepath.Join(dir, "ledger.jsonl"), "history-mcp", box)
	if err != nil {
		t.Fatal("setup:", err)
	}
	defer func() { _ = led.Close() }()
	st := core.NewState("history-mcp", core.DefaultLimits())
	if _, err := led.Replay(st); err != nil {
		t.Fatal("setup:", err)
	}
	return st
}

func TestMailHistoryRealMCPWireBoundAndArguments(t *testing.T) {
	srv, _ := historyServer(t, t.TempDir())
	_, sender := aliasRegister(t, srv, "history-sender", "history-sender-secret")
	_, _ = aliasRegister(t, srv, "history-recipient", "history-recipient-secret")
	aliasCall(t, srv, "send", map[string]any{
		"token": sender, "to": "history-recipient", "type": "notify",
		"body": strings.Repeat("<", 32<<10),
	})
	payload, out := historyExchange(t, srv, "2026-07-28", map[string]any{"token": sender, "include_bodies": true})
	raw, err := json.Marshal(out)
	if err != nil || len(raw)+1 > 128<<10 {
		t.Fatalf("history exceeded actual MCP wire bound: %d, %v", len(raw), err)
	}
	rows, ok := payload["units"].([]any)
	if !ok || len(rows) != 1 {
		t.Fatal("wire bound lost its conversation", payload)
	}
	body := rows[0].(map[string]any)["content"].(map[string]any)["body"].(map[string]any)
	if body["recorded_bytes"] != float64(32<<10) || body["truncated"] != true {
		t.Fatalf("oversized encoded text lost its explicit recorded size: %v", body)
	}
	for _, args := range []map[string]any{
		{"token": "invalid-token"},
		{"token": sender, "limit": 0},
		{"token": sender, "limit": 101},
		{"token": sender, "cursor": "not-a-cursor"},
	} {
		r := toolCall(t, srv, "mail_history", args)
		if r["__is_error"] != true || r["hint"] == nil {
			t.Fatalf("history accepted invalid argument or omitted corrective hint: %v", r)
		}
	}
	encoded, _ := json.Marshal(map[string]any{"token": sender, "limit": 0})
	wrapped := rpc(t, srv, "2025-11-25", "tools/call", map[string]any{"name": "mail_history", "arguments": string(encoded)})
	if result, ok := wrapped["result"].(map[string]any); ok {
		if result["isError"] != true {
			t.Fatal("string-wrapped explicit zero was accepted:", wrapped)
		}
	} else {
		// MCP's HTTP parameter validator may reject the string before the tool
		// decoder. That must be an explicit protocol error with a correction.
		refusal, ok := wrapped["error"].(map[string]any)
		data, dataOK := refusal["data"].(map[string]any)
		if !ok || refusal["code"] != float64(-32602) || !dataOK || data["hint"] == nil {
			t.Fatal("string-wrapped arguments lost their protocol refusal:", wrapped)
		}
	}
}

func TestMailHistoryRealMCPFixedPrefixAcrossReplay(t *testing.T) {
	dir := t.TempDir()
	srv, stop := historyServer(t, dir)
	_, sender := aliasRegister(t, srv, "history-sender", "history-sender-secret")
	_, recipient := aliasRegister(t, srv, "history-recipient", "history-recipient-secret")
	parent := aliasCall(t, srv, "send", map[string]any{"token": sender, "to": "history-recipient", "type": "question", "body": "original"})["msg_serial"]
	aliasCall(t, srv, "respond", map[string]any{"token": recipient, "msg_serial": parent, "disposition": "answer", "body": "answer"})
	first := historyCall(t, srv, "2026-07-28", map[string]any{"token": sender, "limit": 1})
	cursor, ok := first["cursor"].(string)
	if !ok || cursor == "" {
		t.Fatalf("same-conversation pagination missing: %v", first)
	}
	aliasCall(t, srv, "send", map[string]any{"token": sender, "to": "history-recipient", "type": "notify", "body": "after fixed prefix"})
	second := historyCall(t, srv, "2026-07-28", map[string]any{"token": sender, "limit": 1, "cursor": cursor})
	stop()
	restarted, _ := historyServer(t, dir)
	again := historyCall(t, restarted, "2026-07-28", map[string]any{"token": sender, "limit": 1, "cursor": cursor})
	a, _ := json.Marshal(second["units"])
	b, _ := json.Marshal(again["units"])
	if string(a) != string(b) || second["as_of_serial"] != again["as_of_serial"] {
		t.Fatalf("fixed prefix changed across canonical replay: %s != %s", a, b)
	}
	if strings.Contains(string(b), "after fixed prefix") {
		t.Fatal("cursor admitted an append after its fixed upper serial")
	}
}

func TestMailHistoryRealMCPForeignAndForgedCursorsRefuseWithoutRows(t *testing.T) {
	srv, _ := historyServer(t, t.TempDir())
	_, sender := aliasRegister(t, srv, "history-sender", "history-sender-secret")
	_, recipient := aliasRegister(t, srv, "history-recipient", "history-recipient-secret")
	_, stranger := aliasRegister(t, srv, "history-stranger", "history-stranger-secret")
	for n := 0; n < 2; n++ {
		aliasCall(t, srv, "send", map[string]any{"token": sender, "to": "history-recipient", "type": "notify", "body": "PRIVATE-CURSOR"})
	}
	first := historyCall(t, srv, "2026-07-28", map[string]any{"token": sender, "limit": 1})
	cursor, ok := first["cursor"].(string)
	if !ok || cursor == "" {
		t.Fatal("setup: valid real cursor missing", first)
	}
	for _, token := range []string{stranger, recipient} {
		res := toolCall(t, srv, "mail_history", map[string]any{"token": token, "cursor": cursor, "limit": 1})
		if token == stranger && (res["__is_error"] != true || res["code"] != "E_HISTORY_CURSOR" || res["units"] != nil) {
			t.Fatal("foreign-party cursor disclosed a row:", res)
		}
		if token == recipient && res["__is_error"] == true {
			t.Fatal("same-conversation recipient cursor should authorize independently", res)
		}
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		t.Fatal("setup:", err)
	}
	for _, field := range []string{"v", "g", "u", "r", "p"} {
		var c map[string]any
		if err := json.Unmarshal(raw, &c); err != nil {
			t.Fatal("setup:", err)
		}
		switch field {
		case "v":
			c[field] = 2
		case "g":
			c[field] = "another-ledger-generation"
		case "u":
			c[field] = float64(1 << 40)
		case "r":
			c[field] = float64(1 << 40)
		case "p":
			c[field] = map[string]any{"op": float64(1 << 40), "msg": 1, "ord": 0}
		}
		encoded, err := json.Marshal(c)
		if err != nil {
			t.Fatal("setup:", err)
		}
		res := toolCall(t, srv, "mail_history", map[string]any{"token": sender, "cursor": base64.RawURLEncoding.EncodeToString(encoded)})
		if res["__is_error"] != true || res["code"] != "E_HISTORY_CURSOR" || res["hint"] == nil || res["units"] != nil {
			t.Fatal("forged cursor accepted or leaked rows:", field, res)
		}
	}
}

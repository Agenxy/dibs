package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/agenxy/dibs/internal/adminpw"
	"github.com/agenxy/dibs/internal/core"
)

func TestHumanQueueLockEntersThroughTheAdminGate(t *testing.T) {
	eng, ctx := testEngine(t)
	lead, err := eng.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "lead"})
	if err != nil {
		t.Fatal(err)
	}
	worker, err := eng.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "worker"})
	if err != nil {
		t.Fatal(err)
	}
	mail, err := eng.Do(ctx, &core.Op{
		Kind: core.OpSendMessage, Token: lead["token"].(string),
		To: "worker", MsgType: core.MsgRequest, Body: "later",
	})
	if err != nil {
		t.Fatal(err)
	}
	n := mail["msg_serial"].(uint64)
	if _, err := eng.Do(ctx, &core.Op{
		Kind: core.OpRespond, Token: worker["token"].(string), MsgSerial: n, Disposition: "queue",
	}); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	hash, err := adminpw.Hash("queue-fixture-password")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "admin.hash"), []byte(hash), 0o600); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	registerAdminAPI(mux, eng)
	gate := newAuthGate("queue-fixture-secret", filepath.Join(dir, "admin.hash"), "127.0.0.1:4777")
	srv := httptest.NewServer(gate.wrap(mux))
	defer srv.Close()
	post := func(permission, password string, held bool) (int, map[string]any) {
		t.Helper()
		body, err := json.Marshal(map[string]any{
			"agent": "worker", "permission": permission, "held": held, "msg_serial": n,
		})
		if err != nil {
			t.Fatal(err)
		}
		req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/admin/permission", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("X-Dibs-Local", "queue-fixture-secret")
		req.Header.Set("X-Dibs-Admin", password)
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		var result map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&result)
		return resp.StatusCode, result
	}
	if status, _ := post(core.PermQueueOrderLock, "", true); status != http.StatusUnauthorized {
		t.Fatalf("board secret alone changed queue policy: %d", status)
	}
	if status, result := post(core.PermQueueOrderLock, "queue-fixture-password", true); status != 200 {
		t.Fatalf("human queue lock failed: %d %v", status, result)
	}
	view, err := eng.GetMessage(ctx, worker["token"].(string), n)
	if err != nil {
		t.Fatal(err)
	}
	m := view["message"].(*core.Message)
	if !m.QueueOrderLocked || m.QueueLockBy != core.HumanActor {
		t.Fatalf("human lock not recorded: locked=%v by=%s", m.QueueOrderLocked, m.QueueLockBy)
	}
	if status, _ := post(core.PermRelocate, "queue-fixture-password", true); status != http.StatusBadRequest {
		t.Fatalf("irrelevant task scope was silently ignored: %d", status)
	}
	if status, result := post(core.PermQueueOrderLock, "queue-fixture-password", false); status != 200 {
		t.Fatalf("human revoke failed: %d %v", status, result)
	}
}

// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/harnessenv"
	"github.com/agenxy/dibs/internal/testcodexipc"
)

func TestNativeWakeFenceHasNoForeignMailboxOracle(t *testing.T) {
	e, ctx := testEngine(t)
	wakes, release, err := e.AttachHostBridge("own-host", []string{"codex"})
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	do := func(op *core.Op) core.Result {
		t.Helper()
		r, err := e.Do(ctx, op)
		if err != nil {
			t.Fatal("setup:", err)
		}
		return r
	}
	worker := do(&core.Op{
		Kind: core.OpRegister, Name: "worker", SessionID: testcodexipc.Thread,
		Agent: &core.AgentInfo{Harness: "codex", Surface: harnessenv.ChatGPTApp, HostID: "own-host"},
	})["token"].(string)
	sender := do(&core.Op{Kind: core.OpRegister, Name: "sender"})["token"].(string)
	sent := do(&core.Op{Kind: core.OpSendMessage, Token: sender, To: "worker", MsgType: core.MsgNotify, Body: "private"})
	var wr engine.WakeRequest
	select {
	case wr = <-wakes:
	case <-time.After(3 * time.Second):
		t.Fatal("no actual request")
	}
	mux := http.NewServeMux()
	registerWakeAPI(mux, e, "fence-secret")
	post := func(host, secret string) (int, string) {
		t.Helper()
		body, err := json.Marshal(map[string]any{"id": wr.ID, "host": host})
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodPost, "/api/wake-owed", bytes.NewReader(body))
		if secret != "" {
			r.Header.Set("X-Dibs-Local", secret)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w.Code, w.Body.String()
	}
	code, unauth := post("own-host", "")
	if code != http.StatusUnauthorized {
		t.Fatalf("unauth fence: %d %s", code, unauth)
	}
	code, foreign := post("foreign-host", "fence-secret")
	if code != http.StatusUnauthorized || foreign != unauth {
		t.Fatalf("foreign fence disclosed state: %d %s vs %s", code, foreign, unauth)
	}
	code, own := post("own-host", "fence-secret")
	if code != http.StatusOK || own != "{\"owed\":true}\n" {
		t.Fatalf("own fence: %d %s", code, own)
	}
	do(&core.Op{Kind: core.OpAckMessage, Token: worker, MsgSerial: sent["msg_serial"].(uint64)})
	code, own = post("own-host", "fence-secret")
	if code != http.StatusOK || own != "{\"owed\":false}\n" {
		t.Fatalf("ack fence: %d %s", code, own)
	}
	if !e.ReportWakeResult(engine.WakeResult{ID: wr.ID, Host: "own-host", OK: true}) {
		t.Fatal("setup report failed")
	}
}

// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/harnessenv"
)

// The capability must enter through the actual subscription. A missing key
// retains the old bridge's in-flight policy; true admits the next arrival now.
func TestNativeAppCapabilityChangesActualWakeStreamAdmission(t *testing.T) {
	for _, native := range []bool{true, false} {
		t.Run(map[bool]string{true: "native", false: "legacy"}[native], func(t *testing.T) {
			srv, eng, _ := newServerWithEngine(t)
			eng.SetHostID("hub")
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			do := func(op *core.Op) core.Result {
				t.Helper()
				r, err := eng.Do(ctx, op)
				if err != nil {
					t.Fatal("setup:", err)
				}
				return r
			}
			do(&core.Op{
				Kind: core.OpRegister, Name: "worker", SessionID: "7c3f0a11-2b44-4d90-9e57-1f2a3b4c5d6e",
				Agent: &core.AgentInfo{Harness: "Codex", Surface: harnessenv.ChatGPTApp, HostID: "laptop"},
			})
			sender := do(&core.Op{Kind: core.OpRegister, Name: "sender"})["token"].(string)
			meta := map[string]any{HostMetaKey: "laptop", WakeHarnessesMetaKey: []string{"Codex"}}
			if native {
				meta["com.dibs/native_app_delivery"] = true
			}
			body, err := json.Marshal(map[string]any{
				"jsonrpc": "2.0", "id": "wake", "method": "subscriptions/listen",
				"params": map[string]any{"_meta": meta, "notifications": map[string]any{"resourceSubscriptions": []string{WakeURI}}},
			})
			if err != nil {
				t.Fatal(err)
			}
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL, bytes.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "text/event-stream")
			resp, err := srv.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = resp.Body.Close() })
			frames := make(chan map[string]any, 8)
			go func() {
				defer close(frames)
				sc := bufio.NewScanner(resp.Body)
				for sc.Scan() {
					data, ok := strings.CutPrefix(sc.Text(), "data: ")
					if !ok {
						continue
					}
					var f map[string]any
					decoder := json.NewDecoder(strings.NewReader(data))
					decoder.UseNumber()
					if decoder.Decode(&f) == nil {
						frames <- f
					}
				}
			}()
			read := func() map[string]any {
				t.Helper()
				select {
				case f := <-frames:
					return f
				case <-time.After(3 * time.Second):
					t.Fatal("no actual stream frame")
					return nil
				}
			}
			if f := read(); f["method"] != "notifications/subscriptions/acknowledged" {
				t.Fatalf("setup ack: %v", f)
			}
			send := func() {
				do(&core.Op{Kind: core.OpSendMessage, Token: sender, To: "worker", MsgType: core.MsgNotify, Body: "mail"})
			}
			report := func(f map[string]any) {
				t.Helper()
				meta := f["params"].(map[string]any)["_meta"].(map[string]any)
				id, parseErr := strconv.ParseUint(string(meta["id"].(json.Number)), 10, 64)
				if parseErr != nil {
					t.Fatal("setup id:", parseErr)
				}
				if !eng.ReportWakeResult(engine.WakeResult{ID: id, Host: "laptop", OK: true}) {
					t.Fatal("setup report refused")
				}
			}
			send()
			first := read()
			send()
			if native {
				report(read())
				report(first)
				return
			}
			select {
			case f := <-frames:
				t.Fatalf("legacy bridge changed its in-flight contract: %v", f)
			case <-time.After(450 * time.Millisecond):
			}
			report(first)
		})
	}
}

// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

//go:build darwin || linux

package mcp

import (
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/antigravity"
	"github.com/agenxy/dibs/internal/core"
)

func TestAntigravityBindingRequiresAppBridgeOnLocalTransport(t *testing.T) {
	const conversation = "01a10520-0000-7000-8000-000000000007"
	const other = "01a10520-0000-7000-8000-000000000008"
	e, replay := sessionRefreshEngine(t)
	res, err := e.Do(t.Context(), &core.Op{
		Kind: core.OpRegister, Name: "worker", SessionID: "host-old-bridge",
		Agent: &core.AgentInfo{Harness: antigravity.Client, HostID: e.HostID()},
	})
	if err != nil {
		t.Fatal(err)
	}
	token := res["token"].(string)
	prev := verifyAntigravityBridge
	verified := false
	verifyAntigravityBridge = func(pid int) (antigravity.Hub, bool) {
		return antigravity.Hub{PID: 140, Start: time.Unix(10, 0)}, verified && pid == os.Getpid()
	}
	t.Cleanup(func() { verifyAntigravityBridge = prev })
	local := localSessionServer(t, e)
	tcp := httptest.NewServer(New(e))
	t.Cleanup(tcp.Close)
	checkIn := func(server *httptest.Server, evidence map[string]any) {
		t.Helper()
		res := toolCallWithMeta(t, server, "check_in", map[string]any{"token": token}, evidence)
		if res["inbox"] == nil {
			t.Fatalf("check_in setup did not succeed (code=%v)", res["code"])
		}
	}
	assertBound := func(want bool) {
		t.Helper()
		board, err := e.Board(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range board["agents"].([]map[string]any) {
			if row["id"] == "worker" {
				if row["antigravity_wake_bound"] != want {
					t.Fatalf("wake evidence = %v, want %t", row["antigravity_wake_bound"], want)
				}
				return
			}
		}
		t.Fatal("worker missing from board")
	}
	meta := map[string]any{
		HostMetaKey: e.HostID(), antigravity.BridgeMeta: true, antigravity.ConversationMeta: conversation,
	}
	checkIn(local, meta)
	assertBound(false)
	verified = true
	checkIn(tcp, meta)
	assertBound(false)
	checkIn(local, meta)
	assertBound(true)
	if got := replay().Agents["worker"].CurrentSession; got != conversation {
		t.Fatalf("verified conversation=%q, want %q", got, conversation)
	}
	meta[antigravity.ConversationMeta] = other
	checkIn(local, meta)
	assertBound(true)
	if got := replay().Agents["worker"].CurrentSession; got != other {
		t.Fatalf("second conversation=%q, want %q", got, other)
	}
	delete(meta, antigravity.ConversationMeta)
	checkIn(local, meta)
	assertBound(false)
}

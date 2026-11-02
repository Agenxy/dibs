// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/ledger"
)

func TestCoordinatorRecoveryV1PendingProposalRequiresRepreview(t *testing.T) {
	for _, era := range []string{"2026-07-28", "2025-11-25"} {
		t.Run(era, func(t *testing.T) {
			f := recoveryFixture(t, era)
			proposal := appendHistoricalV1Recovery(f)
			f.srv, f.e, f.stop = supportedMergeServer(t, f.dir)
			f.call("check_in", map[string]any{"token": f.into["token"]})
			f.call("read_mail", map[string]any{"token": f.into["token"], "msg_serial": proposal})
			if _, err := f.e.Do(context.Background(), &core.Op{Kind: core.OpSweep, StaleAgents: []string{f.into["agent_id"].(string)}}); err != nil {
				t.Fatal(err)
			}
			assertRecoverySurvivorStatus(f, core.StatusDormant)
			before, err := f.e.Board(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			r := f.raw("respond", map[string]any{"token": f.into["token"], "msg_serial": proposal, "disposition": "approve"})
			if r["code"] != "E_RECOVERY_STALE" || !strings.Contains(strings.ToLower(fmt.Sprint(r["hint"])), "preview merge_agents again") {
				t.Fatalf("v1 pending proposal must refuse with corrective coordinator re-preview hint: %v", r)
			}
			after, err := f.e.Board(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if before["serial"] != after["serial"] {
				t.Fatal("legacy refusal recorded consent or changed state")
			}
			assertRecoverySurvivorStatus(f, core.StatusDormant)
			if r := f.raw("read_mail", map[string]any{"token": f.into["token"], "msg_serial": f.mail}); r["code"] == nil {
				t.Fatal("legacy refusal exposed source mail")
			}
		})
	}
}

// This produces a HISTORICAL v1 record, not an alternate current admission
// path. Current preview and consent must both call core.RecoveryProposalFence.
// The pre-change run must actually approve this record: otherwise a malformed
// historical fixture could make the refusal test pass without testing upgrade.
func appendHistoricalV1Recovery(f *supportedMergeFixture) uint64 {
	f.t.Helper()
	f.stop()
	box, err := ledger.LoadOrCreateKey(filepath.Join(f.dir, "key"))
	if err != nil {
		f.t.Fatal(err)
	}
	l, err := ledger.Open(filepath.Join(f.dir, "ledger.jsonl"), "merge-fixture", box)
	if err != nil {
		f.t.Fatal(err)
	}
	s := core.NewState("merge-fixture", core.DefaultLimits())
	if _, err := l.Replay(s); err != nil {
		f.t.Fatal(err)
	}
	from, into := f.source["agent_id"].(string), f.into["agent_id"].(string)
	r := &core.RecoveryRequest{From: from, Into: into, SourceCreated: s.Agents[from].CreatedSerial, SurvivorCreated: s.Agents[into].CreatedSerial, CoordinatorCreated: s.Agents["lead"].CreatedSerial, Fence: historicalV1FixtureFence(f.t, s, from, into)}
	op := &core.Op{Kind: core.OpSendMessage, Token: f.lead["token"].(string), To: into, MsgType: core.MsgRequest, Body: "historical coordinator recovery proposal", Recovery: r}
	now := time.Now()
	if err := s.Admit(op); err != nil {
		f.t.Fatal(err)
	}
	result, _, err := s.Apply(op, now)
	if err != nil {
		f.t.Fatal(err)
	}
	if err := l.Append(s.Serial, now, op); err != nil {
		f.t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		f.t.Fatal(err)
	}
	serial, ok := result["msg_serial"].(uint64)
	if !ok {
		f.t.Fatalf("setup: historical send returned no serial: %v", result)
	}
	return serial
}

// Frozen v1 fixture encoding, deliberately NOT the current projection. This
// fixture has no claims, spaces, predecessor rows or adopted mail. Assert those
// premises rather than hand-copying the general production projection here.
func historicalV1FixtureFence(t *testing.T, s *core.State, from, into string) string {
	t.Helper()
	if len(s.Claims) != 0 || len(s.Spaces) != 0 {
		t.Fatal("setup: v1 fixture unexpectedly has claims or spaces")
	}
	b := *s.Agents[into]
	b.LastCoordination = time.Time{}
	b.Status, b.StaleReason = "", ""
	b.StaleSince, b.DormantSince = time.Time{}, time.Time{}
	b.AckedSerial, b.ContactNoticeReadAt = 0, 0
	h := sha256.New()
	e := json.NewEncoder(h)
	put := func(v any) {
		if err := e.Encode(v); err != nil {
			t.Fatal(err)
		}
	}
	put("recovery-proposal:v1")
	put([]*core.Agent{s.Agents[from], &b})
	keys := make([]uint64, 0, len(s.Messages))
	for n := range s.Messages {
		keys = append(keys, n)
	}
	slices.Sort(keys)
	for _, n := range keys {
		m := s.Messages[n]
		switch {
		case m.To == from || m.To == into:
			if n < s.Agents[m.To].CreatedSerial {
				t.Fatal("setup: fixture has predecessor mail")
			}
			put([]any{"incoming", m})
		case m.From == from:
			if n < s.Agents[from].CreatedSerial {
				t.Fatal("setup: fixture has predecessor outgoing mail")
			}
			put([]any{"outgoing", m.Serial, m.From, m.To})
		}
	}
	put([]*core.Claim{})
	put(map[string]any{})
	nonces := map[string]string{}
	for n, id := range s.Nonces {
		if id == from || id == into {
			nonces[n] = id
		}
	}
	put(nonces)
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

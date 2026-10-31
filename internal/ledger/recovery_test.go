// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package ledger

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// The authority is exercised through MCP tests. Here the actual encrypted
// ledger and compacted snapshot must fold the recorded decision identically.
func TestRecoverySurvivesLedgerReplayAndCompactedSnapshot(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ledger.jsonl")
	box, err := LoadOrCreateKey(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	l, err := Open(path, "recovery", box)
	if err != nil {
		t.Fatal(err)
	}
	s := core.NewState("recovery", core.DefaultLimits())
	old := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for _, id := range []string{"source", "survivor", "lead"} {
		apply(t, s, l, &core.Op{Kind: core.OpRegister, Name: id, Nonce: id + "-nonce", NewToken: id + "-token", AgentKind: core.KindPersistent}, old)
		apply(t, s, l, &core.Op{Kind: core.OpAckBoard, Token: id + "-token"}, old)
	}
	apply(t, s, l, &core.Op{Kind: core.OpSetSlot, Token: "source-token", SlotID: "s1", Text: "old work", Holds: []string{"port:12345"}, Refs: []string{"request:42"}}, old)
	apply(t, s, l, &core.Op{Kind: core.OpSetSlot, Token: "survivor-token", SlotID: "s1", Text: "live work"}, old)
	apply(t, s, l, &core.Op{Kind: core.OpSendMessage, Token: "lead-token", To: "source", MsgType: core.MsgQuestion, Body: "private mail"}, old)
	r := &core.RecoveryRequest{From: "source", Into: "survivor", SourceCreated: s.Agents["source"].CreatedSerial, SurvivorCreated: s.Agents["survivor"].CreatedSerial, CoordinatorCreated: s.Agents["lead"].CreatedSerial, Fence: s.MergeFence("source", "survivor", 0)}
	send := apply(t, s, l, &core.Op{Kind: core.OpSendMessage, Token: "survivor-token", To: "lead", MsgType: core.MsgRequest, Body: "recovery consent", Recovery: r}, old)
	apply(t, s, l, &core.Op{Kind: core.OpRespond, Token: "lead-token", MsgSerial: send["msg_serial"].(uint64), Disposition: "approve", RecoveryActor: &core.MergeActor{ID: "lead", Created: r.CoordinatorCreated, Authority: "coordinator", Evidence: core.MergeEvidence{Known: true}}, RecoveryDeclarationTimes: map[string]time.Time{"s1": old}}, old)
	wantHistory := s.Agents["survivor"].InheritedDeclarations
	wantAddresses := s.RecoveredAddresses
	if len(wantHistory) != 1 || len(s.Agents["source"].Slots) != 0 || s.Agents["survivor"].Slots["s1"].Text != "live work" {
		t.Fatal("setup: recovery did not fold")
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	for pass := 0; pass < 3; pass++ {
		l, err = Open(path, "recovery", box)
		if err != nil {
			t.Fatal(err)
		}
		if pass > 0 {
			l.KeepHistory(time.Hour)
			l.clock = func() time.Time { return old.Add(72 * time.Hour) }
		}
		s = core.NewState("recovery", core.DefaultLimits())
		if _, err := l.Replay(s); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(s.Agents["survivor"].InheritedDeclarations, wantHistory) || !reflect.DeepEqual(s.RecoveredAddresses, wantAddresses) {
			t.Fatalf("pass %d: recovery state changed", pass)
		}
		if pass > 0 {
			if _, ok, err := readSnapshotHeader(filepath.Join(dir, SnapshotName)); err != nil || !ok {
				t.Fatalf("no compacted snapshot: %v", err)
			}
		}
		// Exercise absence explicitly: closed source ids currently remain reserved
		// rows, but routing authority must not depend on keeping one forever.
		delete(s.Agents, "source")
		if got, err := s.RecoveredRecipient("source"); err != nil || got != "survivor" {
			t.Fatalf("routing depended on retained source row: %s %v", got, err)
		}
		before := s.Serial
		for _, op := range []*core.Op{
			{Kind: core.OpRegister, Name: "source", Nonce: "source-nonce"},
			{Kind: core.OpRegister, Name: "source", Nonce: "fresh-nonce"},
			{Kind: core.OpRegister, Name: "SOURCE!!!", Nonce: "fresh-nonce"},
		} {
			if err := s.Admit(op); err == nil {
				t.Fatalf("reserved address/credential reused: %+v", op)
			}
		}
		if s.Serial != before {
			t.Fatal("refusal mutated state")
		}
		if err := s.Admit(&core.Op{Kind: core.OpRegister, Name: "survivor", Nonce: "survivor-nonce"}); err != nil {
			t.Fatal("survivor nonce refused", err)
		}
		if err := l.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

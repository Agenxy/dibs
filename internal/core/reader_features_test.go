// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package core

import (
	"encoding/json"
	"reflect"
	"strconv"
	"testing"
	"time"
)

// Boot alone scans all feature data. Measure the historical census separately
// from the bounded per-operation backstop in the engine benchmark.
func BenchmarkFeatureDataCensus(b *testing.B) {
	for _, count := range []int{16_384, 65_536} {
		b.Run(strconv.Itoa(count), func(b *testing.B) {
			s := NewState("census", DefaultLimits())
			for i := range 64 {
				s.Agents[strconv.Itoa(i)] = &Agent{}
			}
			for i := range count {
				s.Messages[uint64(i+1)] = &Message{Serial: uint64(i + 1)}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if len(s.FeatureData()) != 0 {
					b.Fatal("setup: board unexpectedly has feature data")
				}
			}
		})
	}
}

func TestReaderRequirementChangesOnlyItsReplayableSet(t *testing.T) {
	s := NewState("reader-requirement", DefaultLimits())
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	if _, _, err := s.Apply(&Op{Kind: OpRegister, Name: "borrowed", NewToken: "token"}, now); err != nil {
		t.Fatal(err)
	}
	before, err := json.Marshal(s.Agents["borrowed"])
	if err != nil {
		t.Fatal(err)
	}
	serial := s.Serial
	op := &Op{Kind: OpRequireV17Features, AgentID: "borrowed", RequiredFeatures: []string{FeatureMailboxRecovery}}
	if err := s.Admit(op); err != nil {
		t.Fatal(err)
	}
	_, events, err := s.Apply(op, now.Add(time.Hour))
	after, encodeErr := json.Marshal(s.Agents["borrowed"])
	if err != nil || encodeErr != nil || string(before) != string(after) || len(events) != 0 || s.Serial != serial+1 ||
		!reflect.DeepEqual(s.RequiredFeatures, []string{FeatureMailboxRecovery}) {
		t.Fatalf("marker changed an actor/emitted an action or failed to record its requirement: serial=%d events=%v err=%v/%v", s.Serial, events, err, encodeErr)
	}
	_, duplicateEvents, err := s.Apply(op, now.Add(2*time.Hour))
	if err != nil || duplicateEvents != nil || s.Serial != serial+1 {
		t.Fatalf("duplicate marker mutated state: serial=%d events=%v err=%v", s.Serial, duplicateEvents, err)
	}
}

func TestReaderRequirementShapeIsAdmittedBeforeTheFold(t *testing.T) {
	s := NewState("shape", DefaultLimits())
	for _, op := range []*Op{
		{Kind: OpRequireV17Features},
		{Kind: OpRequireV17Features, RequiredFeatures: []string{"future/v1"}},
		{Kind: OpRequireV17Features, RequiredFeatures: []string{FeatureMailboxRecovery, FeatureMailboxRecovery}},
		{Kind: OpRequireV17Features, RequiredFeatures: []string{FeatureMailboxRecovery, FeatureDesktopApproval}},
		{Kind: OpAckBoard, RequiredFeatures: []string{FeatureMailboxRecovery}},
	} {
		if err := s.Admit(op); err == nil {
			t.Errorf("invalid requirement marker admitted: %+v", op)
		}
	}
	if s.Serial != 0 || len(s.RequiredFeatures) != 0 {
		t.Fatal("admission mutated the fold")
	}
}

func TestRecoveryFeatureCheckInspectsOnlyAffectedRecords(t *testing.T) {
	for _, source := range []string{"new-message", "message", "message-list", "mailbox-list", "actor", "target", "result", "addresses"} {
		t.Run(source, func(t *testing.T) {
			s := NewState("affected", DefaultLimits())
			s.Messages[99] = &Message{Recovery: &RecoveryRequest{}}
			s.Agents["unrelated"] = &Agent{InheritedDeclarations: map[string]InheritedDeclaration{"old": {}}}
			op := &Op{Kind: OpActivityCheckpoint}
			if !s.HasMailboxRecoveryData() || s.HasMailboxRecoveryAt(op, 10, "") {
				t.Fatal("bounded check scanned unrelated data or full census missed it")
			}
			result := ""
			switch source {
			case "new-message":
				s.Messages[10] = &Message{Recovery: &RecoveryRequest{}}
			case "message", "message-list", "mailbox-list":
				s.Messages[11] = &Message{Recovery: &RecoveryRequest{}}
				switch source {
				case "message":
					op.MsgSerial = 11
				case "message-list":
					op.MsgSerials = []uint64{11}
				case "mailbox-list":
					ids := []uint64{11}
					op.MailboxSerials = &ids
				}
			case "actor", "target", "result":
				s.Agents["affected"] = &Agent{InheritedDeclarations: map[string]InheritedDeclaration{"old": {}}}
				switch source {
				case "actor":
					op.AgentID = "affected"
				case "target":
					op.MergeInto = "affected"
				case "result":
					result = "affected"
				}
			case "addresses":
				s.RecoveredAddresses = map[string]RecoveredAddress{"old": {}}
			}
			if !s.HasMailboxRecoveryAt(op, 10, result) {
				t.Fatal("bounded check missed the affected feature record")
			}
		})
	}
}

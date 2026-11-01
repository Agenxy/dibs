// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package core

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

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

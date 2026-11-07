// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package core

import (
	"testing"
	"time"
)

func TestFullBoardPolicyChangesOnlyForNewValue(t *testing.T) {
	s := NewState("policy", DefaultLimits())
	reg(t, s, "lead", "lead-token", t0)
	s.Agents["lead"].Role = RoleCoordinator
	for _, value := range []string{FullBoardDisplaceIdle, FullBoardDisplaceIdle, FullBoardRefuse, FullBoardRefuse} {
		before, policy := s.Serial, s.FullBoardSetting
		op := &Op{Kind: OpSetFullBoardSetting, Token: "lead-token", SettingKey: FullBoardSettingKey, SettingValue: value}
		if err := s.Admit(op); err != nil {
			t.Fatal(err)
		}
		_, events, err := s.Apply(op, t0.Add(time.Duration(before)*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		if policy.Value == value {
			if s.Serial != before || s.FullBoardSetting != policy || len(events) != 0 {
				t.Fatal("same policy rewrote durable state")
			}
		} else if s.Serial != before+1 || s.FullBoardSetting.By != "lead" || len(events) != 1 {
			t.Fatal("changed policy lost provenance/serial")
		}
	}
}

func TestFullBoardAdmissionRejectsForgedOrProtectedVictim(t *testing.T) {
	s := NewState("policy", DefaultLimits())
	reg(t, s, "old", "old-token", t0)
	s.Limits.MaxAgents = 1
	l := s.Agents["old"]
	l.Status = StatusDormant
	valid := AgentDisplacement{ID: l.ID, CreatedSerial: l.CreatedSerial, LastActivity: l.LastCoordination, Process: ProcessUnknown}
	for _, mutate := range []func(*Op){
		func(o *Op) { o.Displaced.CreatedSerial++ },
		func(o *Op) { o.Displaced.Process = "alive" },
		func(o *Op) { o.Displaced.LastActivity = t0.Add(-time.Hour) },
		func(o *Op) { o.FullBoardPolicy = FullBoardRefuse },
		func(o *Op) { o.Kind = OpHeartbeat },
	} {
		d := valid
		op := &Op{Kind: OpRegister, Name: "new", FullBoardPolicy: FullBoardDisplaceIdle, Displaced: &d}
		mutate(op)
		before := s.Serial
		if err := s.Admit(op); err == nil {
			t.Fatalf("bad displacement admitted: %+v", op)
		}
		if s.Serial != before || l.Status != StatusDormant {
			t.Fatal("admission mutated state")
		}
	}
	op := &Op{Kind: OpRegister, Name: "new", FullBoardPolicy: FullBoardDisplaceIdle, Displaced: &valid}
	if err := s.Admit(op); err != nil {
		t.Fatal("eligible decision refused:", err)
	}
	l.Role = RoleCoordinator
	if err := s.Admit(op); err == nil {
		t.Fatal("coordinator displacement admitted")
	}
}

// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package core

import (
	"fmt"
	"reflect"
	"testing"
	"time"
)

func recoveryState() *State {
	s := NewState("recovery", DefaultLimits())
	old := time.Unix(1000, 0)
	s.Agents["source"] = &Agent{ID: "source", Name: "old-role", Kind: KindPersistent, Status: StatusDormant, CreatedSerial: 1, LastCoordination: old, Agent: &AgentInfo{HostID: "h", Harness: "Codex", Surface: "cli"}, Slots: map[string]Slot{}}
	s.Agents["survivor"] = &Agent{ID: "survivor", Name: "new-role", Kind: KindPersistent, Status: StatusActive, CreatedSerial: 2, LastCoordination: old.Add(time.Minute), Agent: &AgentInfo{HostID: "h", Harness: "Codex", Surface: "cli"}, SessionID: "survivor-session", CurrentSession: "survivor-session", Slots: map[string]Slot{}}
	return s
}

func TestRecoveryHistoryCapacityPreservesFullSourceAndRefusesWithoutMutation(t *testing.T) {
	s := recoveryState()
	a, b := s.Agents["source"], s.Agents["survivor"]
	a.InheritedDeclarations = map[string]InheritedDeclaration{}
	b.InheritedDeclarations = map[string]InheritedDeclaration{}
	for i := 0; i < s.Limits.MaxSlotsPerAgent; i++ {
		id := fmt.Sprint("s", i)
		a.Slots[id] = Slot{ID: id, Text: "old", UpdatedSerial: 3}
	}
	for i := 0; i < MaxInheritedDeclarations-len(a.Slots); i++ {
		b.InheritedDeclarations[fmt.Sprint("inherited:", i)] = InheritedDeclaration{Source: "earlier", SourceCreated: 1, Declaration: Slot{ID: "s1", UpdatedSerial: 1}}
	}
	if err := s.RecoveryEligibility("source", "survivor", MergeEvidence{Known: true}, "h", "h"); err != nil {
		t.Fatal("full source allowance must fit", err)
	}
	a.InheritedDeclarations["old-history"] = InheritedDeclaration{Source: "ancestor", SourceCreated: 1, Declaration: Slot{ID: "s1", UpdatedSerial: 1}}
	before := s.MergeFence("source", "survivor", 0)
	if err := s.RecoveryEligibility("source", "survivor", MergeEvidence{Known: true}, "h", "h"); err == nil {
		t.Fatal("over-cap recovery accepted")
	}
	if after := s.MergeFence("source", "survivor", 0); before != after {
		t.Fatal("capacity refusal changed source or survivor")
	}
	delete(b.InheritedDeclarations, "inherited:0")
	wantOld := a.InheritedDeclarations["old-history"]
	b.Slots["s1"] = Slot{ID: "s1", Text: "live", UpdatedSerial: 9}
	_, _ = s.applyRecovery(&RecoveryRequest{From: "source", Into: "survivor"}, &MergeActor{ID: "lead", Authority: "coordinator"}, map[string]time.Time{}, time.Unix(2000, 0))
	if len(b.InheritedDeclarations) != MaxInheritedDeclarations || len(a.Slots) != 0 || len(a.InheritedDeclarations) != 0 || b.Slots["s1"].Text != "live" {
		t.Fatal("history lost, duplicated or activated")
	}
	found := false
	for _, entry := range b.InheritedDeclarations {
		if reflect.DeepEqual(entry, wantOld) {
			found = true
		}
	}
	if !found {
		t.Fatal("inherited provenance changed during repeated recovery")
	}
}

func TestRecoveryRefusesUnknownIdentityPrivilegedSourcesAndAddressCollisions(t *testing.T) {
	for _, name := range []string{"unknown-host", "unknown-harness", "foreign-harness", "foreign-surface", "ephemeral-source", "privileged-source", "live-source", "bound-source", "same-age", "ambiguous-name", "address-capacity", "claim-conflict"} {
		t.Run(name, func(t *testing.T) {
			s := recoveryState()
			a, b := s.Agents["source"], s.Agents["survivor"]
			host := "h"
			switch name {
			case "unknown-host":
				host = ""
			case "unknown-harness":
				a.Agent.Harness = ""
			case "foreign-harness":
				a.Agent.Harness = "Claude Code"
			case "foreign-surface":
				a.Agent.Surface = "app"
			case "ephemeral-source":
				a.Kind = KindEphemeral
			case "privileged-source":
				a.Role = RoleCoordinator
			case "live-source":
				a.Status = StatusActive
			case "bound-source":
				a.CurrentSession = "old-session"
			case "same-age":
				b.LastCoordination = a.LastCoordination
			case "ambiguous-name":
				s.Agents["namesake"] = &Agent{ID: "namesake", Name: a.Name}
			case "claim-conflict":
				// Sweep releases current claims; this retained-state case protects
				// older snapshots or embedders with dormant conflicting claims.
				s.Claims = []*Claim{{Agent: "source", Path: "/repo", Host: "h"}, {Agent: "survivor", Path: "/repo", Host: "h"}}
			case "address-capacity":
				s.RecoveredAddresses = map[string]RecoveredAddress{}
				for i := 0; i < MaxRecoveredAddresses; i++ {
					s.RecoveredAddresses[fmt.Sprint(i)] = RecoveredAddress{Into: "other"}
				}
			}
			before := s.MergeFence("source", "survivor", 0)
			if err := s.RecoveryEligibility("source", "survivor", MergeEvidence{Known: true}, host, "h"); err == nil {
				t.Fatal("unsafe recovery accepted")
			}
			if s.MergeFence("source", "survivor", 0) != before {
				t.Fatal("refusal changed state")
			}
		})
	}
}

func TestRecoveredAddressChainAndRetiredSurvivor(t *testing.T) {
	s := recoveryState()
	s.RecoveredAddresses = map[string]RecoveredAddress{
		"ancestor": {Source: "ancestor", SourceCreated: 3, Into: "source", SurvivorCreated: 1},
		"source":   {Source: "source", SourceCreated: 1, Into: "survivor", SurvivorCreated: 2},
	}
	delete(s.Agents, "source")
	if got, err := s.RecoveredRecipient("ancestor"); err != nil || got != "survivor" {
		t.Fatalf("chain lost after source absence: %s %v", got, err)
	}
	s.Agents["survivor"].Status = StatusClosed
	if _, err := s.RecoveredRecipient("ancestor"); err == nil {
		t.Fatal("retired destination accepted")
	}
	s.Agents["survivor"].Status = StatusActive
	s.Agents["survivor"].CreatedSerial = 4
	if _, err := s.RecoveredRecipient("ancestor"); err == nil {
		t.Fatal("reused destination incarnation inherited mail route")
	}
}

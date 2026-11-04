// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package core

import (
	"encoding/json"
	"testing"
	"time"
)

// Decode the recorded wire field, so old-code controls compile and fail on
// behavior rather than on an identifier introduced with the implementation.
func mergeAdoptionOp(t *testing.T, serials []uint64) *Op {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"kind": OpMergeAgents, "to": "fork", "merge_into": "survivor",
		"merge_actor":     map[string]any{"id": "lead", "authority": "coordinator", "evidence": map[string]any{"known": true}},
		"merge_adoptions": serials,
	})
	if err != nil {
		t.Fatal(err)
	}
	var op Op
	if err := json.Unmarshal(raw, &op); err != nil {
		t.Fatal(err)
	}
	return &op
}

func mergeAdoptionState(t *testing.T) (*State, time.Time) {
	t.Helper()
	now := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	s := NewState("merge-adoption", DefaultLimits())
	seat(t, s, "fork", "fork-secret", now)
	seat(t, s, "survivor", "survivor-secret", now)
	seat(t, s, "lead", "lead-secret", now)
	s.Agents["fork"].Status = StatusDormant
	s.Serial = 20
	s.Messages[10] = &Message{Serial: 10, From: "survivor", To: "lead", Type: MsgRequest, Adopt: "fork", State: MsgStatePending}
	return s, now
}

func TestMergeAdoptionRecordedSelectionAndHistoricalFold(t *testing.T) {
	for _, recorded := range []bool{false, true} {
		t.Run(map[bool]string{false: "historical", true: "recorded"}[recorded], func(t *testing.T) {
			s, now := mergeAdoptionState(t)
			var serials []uint64
			if recorded {
				serials = []uint64{10}
			}
			op := mergeAdoptionOp(t, serials)
			if err := s.Admit(op); err != nil {
				t.Fatal("setup admission:", err)
			}
			// Replay must fold the recorded choice even if today's selector
			// would now choose differently. Validation is never added to Apply.
			if recorded {
				s.Agents["survivor"].CreatedSerial = 11
			}
			_, events, err := s.Apply(op, now)
			if err != nil || s.Serial != 21 || s.Agents["fork"].MergedInto != "survivor" {
				t.Fatalf("merge fold/serial changed: %v serial=%d", err, s.Serial)
			}
			m := s.Messages[10]
			if recorded {
				if m.State != MsgStateApproved || m.RespondedAt != 21 || !m.Consumed {
					t.Fatalf("recorded closure not folded: %+v", m)
				}
				count := 0
				for _, event := range events {
					if event.Type == "message.approved" {
						count++
						if event.Agent != "lead" || event.To != "survivor" || event.Serial != 21 || event.Data["by"] != "lead" {
							t.Fatalf("closure misattributed or separately serialed: %+v", event)
						}
					}
				}
				if count != 1 {
					t.Fatalf("approval events=%d want1", count)
				}
			} else if m.State != MsgStatePending || m.Consumed || m.RespondedAt != 0 {
				t.Fatalf("historical merge acquired new closure semantics: %+v", m)
			}
		})
	}
}

func TestMergeAdoptionAdmissionRejectsWrongRecordedSelection(t *testing.T) {
	for _, invalid := range []string{"source-incarnation", "survivor-incarnation", "rewritten-requester", "different-source", "different-requester", "combined-grant", "combined-retirement", "final", "absent", "duplicate", "wrong-op"} {
		t.Run(invalid, func(t *testing.T) {
			s, _ := mergeAdoptionState(t)
			op := mergeAdoptionOp(t, []uint64{10})
			switch invalid {
			case "source-incarnation":
				s.Agents["fork"].CreatedSerial = 11
			case "survivor-incarnation":
				s.Agents["survivor"].CreatedSerial = 11
			case "rewritten-requester":
				s.Messages[10].MergedSentAt = 12
			case "different-source":
				s.Messages[10].Adopt = "another"
			case "different-requester":
				s.Messages[10].From = "another"
			case "combined-grant":
				s.Messages[10].Grant = RoleCoordinator
			case "combined-retirement":
				s.Messages[10].Merge = &MergeRequest{From: "fork", Into: "survivor"}
			case "final":
				s.Messages[10].State = MsgStateDenied
			case "absent":
				delete(s.Messages, 10)
			case "duplicate":
				op = mergeAdoptionOp(t, []uint64{10, 10})
			case "wrong-op":
				op.Kind = OpActivityCheckpoint
			}
			if err := s.Admit(op); err == nil {
				t.Fatalf("%s recorded closure admitted", invalid)
			}
			if s.Serial != 20 || s.Agents["fork"].Status != StatusDormant {
				t.Fatal("admission mutated state")
			}
		})
	}
}

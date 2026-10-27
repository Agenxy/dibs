// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package core

import (
	"testing"
	"time"
)

func adoptionCompletionState(t *testing.T) (*State, uint64, uint64, uint64) {
	t.Helper()
	s := NewState("completion", DefaultLimits())
	for _, id := range []string{"lost", "heir", "lead", "sender"} {
		reg(t, s, id, "token-"+id, t0)
		mustApply(t, s, &Op{Kind: OpAckBoard, Token: "token-" + id}, t0)
	}
	mail := mustApply(t, s, &Op{Kind: OpSendMessage, Token: "token-sender", To: "lost", MsgType: MsgRequest, Body: "unread"}, t0)["msg_serial"].(uint64)
	request := mustApply(t, s, &Op{Kind: OpSendMessage, Token: "token-heir", To: "lead", MsgType: MsgRequest, Body: "recover", Adopt: "lost"}, t0)["msg_serial"].(uint64)
	contact := mustApply(t, s, &Op{Kind: OpContactEscalate, MsgSerial: mail}, t0)["contact_serial"].(uint64)
	mustApply(t, s, &Op{Kind: OpSweep, StaleAgents: []string{"lost"}}, t0)
	return s, mail, request, contact
}

func TestAdoptionRecoveryFlagPreservesHistoricalFolds(t *testing.T) {
	for _, route := range []string{"direct", "approval"} {
		for _, recovery := range []bool{false, true} {
			t.Run(route+"/"+itoaBool(recovery), func(t *testing.T) {
				s, mail, request, contact := adoptionCompletionState(t)
				op := &Op{Kind: OpAdoptAgent, Token: "token-lead", To: "lost", Space: "heir", AdoptAuthorised: true, V7Semantics: true, AdoptionRecovery: recovery}
				if route == "approval" {
					op.Kind, op.MsgSerial, op.Disposition = OpRespond, request, "approve"
				}
				before := s.Serial
				_, evs, err := s.Apply(op, t0.Add(time.Second))
				if err != nil {
					t.Fatal(err)
				}
				if s.Serial != before+1 || s.Messages[mail].To != "heir" {
					t.Fatal("one adoption must move mail in exactly one serial")
				}
				if s.Contacts[contact].ResolvedAt.IsZero() == recovery || (s.Messages[mail].ContactEscalatedAt == 0) != recovery {
					t.Fatalf("recovery=%v changed the wrong historical contact semantics: %+v %+v", recovery, s.Contacts[contact], s.Messages[mail])
				}
				resolved := false
				for _, ev := range evs {
					if ev.Serial != s.Serial {
						t.Fatalf("effect escaped adoption serial: %+v", ev)
					}
					if ev.Type == "contact.resolved" && ev.Data["contact_serial"] == contact {
						resolved = true
					}
				}
				if resolved != recovery {
					t.Fatalf("contact resolution event=%v recovery=%v", resolved, recovery)
				}
				want := uint64(0)
				if recovery && route == "direct" {
					want = s.Serial
				}
				if s.Messages[request].PriorAdoptionSerial != want {
					t.Fatalf("request receipt=%d want=%d", s.Messages[request].PriorAdoptionSerial, want)
				}
			})
		}
	}
}

func TestPriorAdoptionAdmissionRequiresExactRequestAndIncarnations(t *testing.T) {
	for _, change := range []string{"valid", "wrong-serial", "new-source", "new-heir", "new-request", "merged-sender", "remaining-mail", "missing-evidence", "not-authorized", "wrong-verb", "unflagged"} {
		t.Run(change, func(t *testing.T) {
			s, mail, request, _ := adoptionCompletionState(t)
			mustApply(t, s, &Op{Kind: OpAdoptAgent, Token: "token-lead", To: "lost", Space: "heir", AdoptAuthorised: true, V7Semantics: true, AdoptionRecovery: true}, t0)
			prior := s.Serial
			// The request receipt must outlive consumption/retention of moved mail.
			delete(s.Messages, mail)
			op := &Op{Kind: OpRespond, Token: "token-lead", MsgSerial: request, Disposition: "approve", AdoptAuthorised: true, AdoptionRecovery: true, PriorAdoptionSerial: prior}
			switch change {
			case "valid":
			case "wrong-serial":
				op.PriorAdoptionSerial++
			case "new-source":
				s.Agents["lost"].CreatedSerial = prior + 1
			case "new-heir":
				s.Agents["heir"].CreatedSerial = prior + 1
			case "new-request":
				s.Messages[request].Serial = prior + 1
			case "merged-sender":
				s.Messages[request].MergedSentAt = prior + 1
			case "remaining-mail":
				mustApply(t, s, &Op{Kind: OpSendMessage, Token: "token-sender", To: "lost", MsgType: MsgRequest, Body: "new work"}, t0)
			case "missing-evidence":
				s.Messages[request].PriorAdoptionSerial = 0
			case "not-authorized":
				op.AdoptAuthorised = false
			case "wrong-verb":
				op.Disposition = "done"
			case "unflagged":
				op.AdoptionRecovery = false
			}
			before := s.Serial
			err := s.Admit(op)
			if change == "valid" && err != nil || change != "valid" && codeOf(err) != "E_BAD_ADOPTION" {
				t.Fatalf("%s admission: %v", change, err)
			}
			if s.Serial != before {
				t.Fatal("admission changed replayable state")
			}
		})
	}
}

func itoaBool(b bool) string {
	if b {
		return "new"
	}
	return "historical"
}

// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package core

import "testing"

func TestRecoveryAddressReservationsUseIngressPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name, address, peerID, peerName string
		peerStatus                      AgentStatus
		allow                           bool
	}{
		{"exact ID before closed namesake", "source", "peer", "source", StatusClosed, true},
		{"exact ID before live namesake", "source", "peer", "source", StatusActive, true},
		{"live label before closed label", "old-role", "peer", "old-role", StatusClosed, true},
		{"competing live label", "old-role", "peer", "old-role", StatusActive, false},
		{"closed stable ID still owns its address", "old-role", "old-role", "other", StatusClosed, false},
		{"survivor stable ID cannot redirect to itself", "survivor", "peer", "other", StatusClosed, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := recoveryState()
			s.Agents["source"].Name = tc.address
			s.Agents[tc.peerID] = &Agent{ID: tc.peerID, Name: tc.peerName, Status: tc.peerStatus}
			before := s.RecoveryProposalFence("source", "survivor", 0)
			err := s.RecoveryEligibility("source", "survivor", MergeEvidence{Known: true}, "h", "h")
			if (err == nil) != tc.allow {
				t.Fatalf("allow=%t: %v", tc.allow, err)
			}
			if s.RecoveryProposalFence("source", "survivor", 0) != before {
				t.Fatal("address eligibility changed replayable state")
			}
		})
	}
}

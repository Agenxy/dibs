// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package core

import (
	"testing"
	"time"
)

// Recorded on b9f26269 before the proposal-only projection changed. These
// literal fingerprints protect the other two paths, including mail, private
// nonce mappings and both rows; recomputing an expected value would hide drift.
func TestRecoveryProposalChangePreservesMergeAndSelfFenceFingerprints(t *testing.T) {
	s := recoveryState()
	// The general fixture uses time.Local. A wire fingerprint must pin its
	// own offsets, independent of the machine running the guard.
	s.Agents["source"].LastCoordination = time.Unix(1000, 0).UTC()
	s.Agents["survivor"].LastCoordination = time.Unix(1000, 0).UTC().Add(time.Minute)
	s.Serial = 20
	s.Agents["source"].Slots["s1"] = Slot{ID: "s1", Text: "source work", UpdatedSerial: 3}
	s.Agents["survivor"].Slots["s2"] = Slot{ID: "s2", Text: "survivor work", UpdatedSerial: 4}
	s.Nonces["source-fixture-nonce"], s.Nonces["survivor-fixture-nonce"] = "source", "survivor"
	s.Claims = []*Claim{{Agent: "source", Path: "/fixture/source", Host: "h", Mode: "shared"}}
	s.Spaces["fixture"] = &Space{ID: "fixture", Owner: "source", Members: map[string]*Membership{"source": {Agent: "source", JoinedSerial: 3}}, Subs: map[string]bool{"source": true}}
	s.Messages[10] = &Message{Serial: 10, To: "source", From: "peer", Type: MsgQuestion, Body: "source incoming"}
	s.Messages[11] = &Message{Serial: 11, To: "survivor", From: "source", Type: MsgQuestion, Body: "source outgoing to survivor"}
	s.Messages[12] = &Message{Serial: 12, To: "peer", From: "survivor", Type: MsgNotify, Body: "survivor outgoing"}
	s.Messages[13] = &Message{Serial: 13, To: "survivor", From: "peer", Type: MsgNotify, Body: "survivor incoming"}
	s.Messages[14] = &Message{Serial: 14, To: "survivor", From: "lead", Type: MsgRequest, Body: "approval envelope"}
	for _, tc := range []struct {
		ignore uint64
		want   string
	}{{0, "sha256:459aed09bc78dfcf181336e868c556d470862e6b878b0778b669222585d8558c"}, {14, "sha256:3fc9177529463d7177d607e28576a2d087911591e9bd68a52b4aa4f6afc6e1ba"}} {
		if got := s.MergeFence("source", "survivor", tc.ignore); got != tc.want {
			t.Errorf("human merge ignore=%d: got %q want %q", tc.ignore, got, tc.want)
		}
		r := &RecoveryRequest{From: "source", Into: "survivor"}
		if got := s.RecoveryFence(r, "survivor", tc.ignore); got != tc.want {
			t.Errorf("survivor-initiated ignore=%d: got %q want %q", tc.ignore, got, tc.want)
		}
	}
	// The v1 literal was measured through the public old projection at
	// b9f26269. A common-encoder edit must fail here before rewriting history.
	legacy := &RecoveryRequest{From: "source", Into: "survivor", Fence: "sha256:" + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	if got := s.recordedRecoveryFence(legacy, "lead", 14); got != "sha256:bd28e79aad8c6f761ad1b14b07905a084f1da592871e0a0479d2f2e860544b81" {
		t.Fatalf("frozen v1 fold changed: %s", got)
	}
	const wantV2 = "v2:sha256:336644c37317eb48a47e70251dea06247c4282662ca917464626a361ecdda995"
	if got := s.RecoveryProposalFence("source", "survivor", 14); got != wantV2 {
		t.Fatalf("v2 projection changed: %s", got)
	}
	current := *legacy
	current.Fence = "v2:" + legacy.Fence
	if got := s.recordedRecoveryFence(&current, "lead", 14); got != wantV2 {
		t.Fatalf("recorded v2 fold differs from current projection: %s", got)
	}
}

func TestRecoveryProposalFenceKeepsSourceSenderRewriteToSurvivor(t *testing.T) {
	for _, tc := range []struct {
		from, to string
		same     bool
	}{
		{"peer", "survivor", true},
		{"source", "survivor", false},
		{"source", "peer", false},
		{"survivor", "peer", true},
		{"survivor", "source", false},
	} {
		t.Run(tc.from+"-to-"+tc.to, func(t *testing.T) {
			s := recoveryState()
			before := s.RecoveryProposalFence("source", "survivor", 0)
			s.Messages[10] = &Message{Serial: 10, From: tc.from, To: tc.to, Type: MsgNotify, Body: "new mail"}
			after := s.RecoveryProposalFence("source", "survivor", 0)
			if (before == after) != tc.same {
				t.Fatalf("same=%t for %s to %s: before=%s after=%s", tc.same, tc.from, tc.to, before, after)
			}
		})
	}
}

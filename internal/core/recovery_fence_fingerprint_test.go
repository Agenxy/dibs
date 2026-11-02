// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package core

import "testing"

// Recorded on b9f26269 before the proposal-only projection changed. These
// literal fingerprints protect the other two paths, including mail, private
// nonce mappings and both rows; recomputing an expected value would hide drift.
func TestRecoveryProposalChangePreservesMergeAndSelfFenceFingerprints(t *testing.T) {
	s := recoveryState()
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
	}{{0, "sha256:cb76b4121972c5b940f8d5932b2dc6e9fbce0f02d85098de763a96bb6c83e1bb"}, {14, "sha256:bfbca0a709d4390e4a4d9e1f0ddc3c37080266a2b6d1fbefc7f4cb01d4a9a28a"}} {
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
	if got := s.recordedRecoveryFence(legacy, "lead", 14); got != "sha256:a88f3620f125c2ba5b52cc97a205a512d1acf88bae7339ac64f91b80fdea942a" {
		t.Fatalf("frozen v1 fold changed: %s", got)
	}
	const wantV2 = "v2:sha256:9eb4ebfbe31a3faeced1cf1090fa12360fc816b686db3781392b26d29ec3f998"
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

// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package core

// MergedNonce is durable negative authority, retained beyond source-row GC.
// The map is private replayable state, encrypted with the rest of the ledger.
// Dropping it would let the same credential mint a new fork after retention.
type MergedNonce struct {
	Displaced bool   `json:"displaced,omitempty"`
	From      string `json:"from"`
	Into      string `json:"into"`
	By        string `json:"by"`
	Serial    uint64 `json:"serial"`
}

// MergedNonceError is ingress policy, never a condition in the replay fold.
// A credential which already resolves to a survivor takes precedence. Group
// recovery selects its valid canonical mapping before asking about tombstones.
func (s *State) MergedNonceError(nonce string) error {
	if nonce == "" || s.Agents[s.Nonces[nonce]] != nil {
		return nil
	}
	t, ok := s.MergedNonces[nonce]
	if !ok {
		return nil
	}
	if t.Displaced {
		return errf("E_AGENT_DISPLACED", "this dormant seat was displaced under limits.full_board. "+
			"Register again with a new nonce to create a new identity; ask the human to inspect the old mail with all_mail",
			"agent %s was displaced to admit %s", t.From, t.Into)
	}
	return errf("E_SEAT_MERGED", "this seat was merged into "+t.Into+" by "+t.By+
		"; reattach with "+t.Into+"'s own nonce", "seat %s was merged into %s by %s", t.From, t.Into, t.By)
}

func (s *State) moveMergeCredentials(from, into string, actor *MergeActor) {
	for nonce, id := range s.Nonces {
		if id != from {
			continue
		}
		if actor.Authority == "human" || actor.Authority == "admin" {
			s.Nonces[nonce] = into
			continue
		}
		if s.MergedNonces == nil { // pre-feature state/snapshot
			s.MergedNonces = map[string]MergedNonce{}
		}
		s.MergedNonces[nonce] = MergedNonce{From: from, Into: into, By: actor.ID, Serial: s.Serial + 1}
		delete(s.Nonces, nonce)
	}
}

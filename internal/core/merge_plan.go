// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package core

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"slices"
)

// MergeRequest is a typed human decision, not participant prose. Fence binds
// the complete movable inputs; it never grants permission to read a mailbox.
type MergeRequest struct {
	From  string `json:"from"`
	Into  string `json:"into"`
	Fence string `json:"fence"`
}

// MergeActor is supplied by the engine after authenticating the current caller.
// Its presence selects the new ownership-aware fold; historical merges omit it.
type MergeActor struct {
	ID        string        `json:"id"`
	Created   uint64        `json:"created_serial"`
	Authority string        `json:"authority"`
	Evidence  MergeEvidence `json:"evidence"`
}

// MergeEvidence is observed outside core. Unknown cannot prove a process dead.
type MergeEvidence struct {
	Known     bool   `json:"known"`
	Live      bool   `json:"live"`
	Source    string `json:"source"`
	Created   uint64 `json:"created_serial"`
	Host      string `json:"host"`
	PID       int    `json:"pid"`
	ProcStart int64  `json:"proc_start"`
}

// MergeEligibility is the ONE coordinator rule, including its human-readable
// refusal. It makes no process or filesystem observations itself.
func (s *State) MergeEligibility(from, into string, evidence MergeEvidence) error {
	if err := s.MergeSafety(from, into); err != nil {
		return err
	}
	a, b := s.Agents[from], s.Agents[into]
	if !sameMergeIdentity(a, b) || len(a.Slots) != 0 || a.hasSessionBinding() || a.CurrentSession != "" ||
		!b.LastCoordination.After(a.LastCoordination) || !evidence.Known || evidence.Live {
		return errf("E_MERGE_NEEDS_HUMAN",
			"use the preview's human_request: send(to:\"human\", type:\"request\", merge:{from:\""+from+
				"\", into:\""+into+"\", fence:<preview fence>}, body:...); their Approve performs the merge",
			"coordinator merge requires matching known name/host/harness/surface/kind, "+
				"an unbound fork without declarations or a live process, and a more recently coordinating survivor")
	}
	return nil
}

func sameMergeIdentity(a, b *Agent) bool {
	if a.Agent == nil || b.Agent == nil {
		return false
	}
	return a.Name != "" && a.Name == b.Name && a.Kind == b.Kind &&
		a.Agent.HostID != "" && a.Agent.HostID == b.Agent.HostID &&
		a.Agent.Harness != "" && a.Agent.Harness == b.Agent.Harness &&
		a.Agent.Surface != "" && a.Agent.Surface == b.Agent.Surface
}

// MergeSafety is current ingress policy, NEVER a new replay condition.
func (s *State) MergeSafety(from, into string) error {
	if from == into {
		return errf("E_BAD_TARGET", "choose two different agents", "cannot merge an agent into itself")
	}
	if err := s.supportedMergeSafety(from, into); err != nil {
		return err
	}
	if s.Agents[from].MergedInto != "" || s.Agents[into].Gone() {
		return errf("E_AGENT_CLOSED", "choose a recoverable source and a live survivor",
			"agent is already merged or survivor retired")
	}
	return nil
}

// MergePlan returns counts, never message content or credentials. ignore names
// this action's own approval envelope, whose response performs the move.
func (s *State) MergePlan(from, into string, ignore uint64) Result {
	counts, owed, outgoing := s.mergeMailCounts(from, ignore)
	claims := s.mergeClaims(from, "")
	nonces := 0
	spaces := []string{}
	for id, ch := range s.Spaces {
		if mergeSpaceReferences(ch, from) {
			spaces = append(spaces, id)
		}
	}
	slices.Sort(spaces)
	for _, id := range s.Nonces {
		if id == from {
			nonces++
		}
	}
	return Result{
		"ok": true, "preview": true, "from": from, "into": into, "messages": counts, "owed_requests": owed,
		"outgoing_rewrites": outgoing, "claims": claims, "spaces": spaces,
		"nonce_mappings": nonces,
		"source":         mergeRowSummary(s.Agents[from]), "survivor": mergeRowSummary(s.Agents[into]),
		"fence_serial": s.Serial, "fence": s.MergeFence(from, into, ignore),
	}
}

func (s *State) mergeOwnsMail(from string, m *Message) bool {
	a := s.Agents[from]
	return a != nil && m.To == from &&
		((m.Serial >= s.mailFloor(from) && m.Serial >= a.CreatedSerial) || s.adoptedFor(m, from))
}

// MergeFence hashes deterministic projected inputs, not the global serial.
// Delivery state on outgoing-only mail does not affect an address rewrite.
// Row incarnation and private nonce mappings are covered, but never exposed.
func (s *State) MergeFence(from, into string, ignore uint64) string {
	sum := sha256.New()
	enc := newMergeEncoder(sum)
	enc("merge:v1")
	enc([]*Agent{s.Agents[from], s.Agents[into]})
	for _, n := range sortedMessageKeys(s.Messages) {
		if n == ignore {
			continue
		}
		m := s.Messages[n]
		switch {
		case s.mergeOwnsMail(from, m), s.mergeOwnsMail(into, m):
			enc([]any{"incoming", m})
		case s.mergeRewritesSender(from, m):
			enc([]any{"outgoing", m.Serial, m.From, m.To})
		}
	}
	enc(s.mergeClaims(from, into))
	enc(s.mergeMembershipInputs(from, into))
	nonces := map[string]string{}
	for n, id := range s.Nonces {
		if id == from || id == into {
			nonces[n] = id
		}
	}
	enc(nonces)
	return "sha256:" + hex.EncodeToString(sum.Sum(nil))
}

func (s *State) mergeRewritesSender(from string, m *Message) bool {
	a := s.Agents[from]
	return senderOwnsMessage(m, a)
}

func (s *State) mergeMailCounts(from string, ignore uint64) (map[string]map[string]int, int, int) {
	counts := map[string]map[string]int{}
	owed, outgoing := 0, 0
	for _, m := range s.Messages {
		if m.Serial == ignore {
			continue
		}
		if s.mergeOwnsMail(from, m) {
			if counts[m.Type] == nil {
				counts[m.Type] = map[string]int{}
			}
			counts[m.Type][m.State]++
			if m.DurableDebt() {
				owed++
			}
		}
		if s.mergeRewritesSender(from, m) {
			outgoing++
		}
	}
	return counts, owed, outgoing
}

// Copies, because the preview is encoded after the writer releases its state.
func (s *State) mergeClaims(from, into string) []*Claim {
	claims := []*Claim{}
	for _, c := range s.Claims {
		if c.Agent != from && (into == "" || c.Agent != into) {
			continue
		}
		copyClaim := *c
		if c.Repo != nil {
			repo := *c.Repo
			copyClaim.Repo = &repo
		}
		claims = append(claims, &copyClaim)
	}
	slices.SortFunc(claims, func(a, b *Claim) int {
		if n := cmp.Compare(a.Agent, b.Agent); n != 0 {
			return n
		}
		if n := cmp.Compare(a.Path, b.Path); n != 0 {
			return n
		}
		return cmp.Compare(a.Host, b.Host)
	})
	return claims
}

func (s *State) mergeMembershipInputs(from, into string) map[string]any {
	spaces := map[string]any{}
	for id, ch := range s.Spaces {
		if mergeSpaceReferences(ch, from) || mergeSpaceReferences(ch, into) {
			spaces[id] = []any{
				ch.Members[from], ch.Members[into], ch.Subs[from], ch.Subs[into],
				ch.Owner, ch.Queue, ch.Pending[from], ch.Pending[into], ch.Declined[from], ch.Declined[into],
			}
		}
	}
	return spaces
}

func mergeSpaceReferences(ch *Space, id string) bool {
	return ch.Members[id] != nil || ch.Subs[id] || ch.Owner == id || slices.Contains(ch.Queue, id) ||
		ch.Pending[id] != nil || ch.Declined[id]
}

func mergeRowSummary(a *Agent) Result {
	return Result{
		"id": a.ID, "created_serial": a.CreatedSerial, "name": a.Name, "host_id": agentHostID(a),
		"harness": mergeAgentHarness(a), "kind": a.Kind, "role": a.Role, "status": a.Status,
		"bound_session": a.hasSessionBinding() || a.CurrentSession != "", "declarations": len(a.Slots),
		"last_coordination": a.LastCoordination,
	}
}

func mergeAgentHarness(a *Agent) Result {
	if a.Agent == nil {
		return nil
	}
	return Result{"harness": a.Agent.Harness, "surface": a.Agent.Surface}
}

// Unlike the historical fold, current policy compares absolute paths with
// their recorded host. Unknown hosts remain conservatively conflicting.
func (s *State) supportedMergeSafety(from, into string) error {
	a, b := s.Agents[from], s.Agents[into]
	if a == nil || b == nil {
		return errf("E_NO_AGENT", "check both exact ids on the board", "one of the merge identities is absent")
	}
	if a.Status == StatusActive {
		return errf("E_AGENT_LIVE", "stop or sign off the source first", "source %q is active", from)
	}
	for _, c := range s.Claims {
		if c.Agent != from {
			continue
		}
		if s.mergeClaimOn(c, into) != nil {
			return errf("E_CLAIM_CONFLICT", "release one conflicting claim first", "both rows claim %q", c.Path)
		}
	}
	return nil
}

func (s *State) mergeClaimOn(claim *Claim, agent string) *Claim {
	for _, c := range s.Claims {
		if c.Agent == agent && c.Path == claim.Path &&
			(c.Host == "" || claim.Host == "" || c.Host == claim.Host) {
			return c
		}
	}
	return nil
}

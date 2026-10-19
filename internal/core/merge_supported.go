// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package core

import (
	"strings"
	"time"
)

func admitMergeRequest(op *Op, lim Limits) error {
	if op.Merge == nil {
		return nil
	}
	if op.Kind != OpSendMessage || op.MsgType != MsgRequest || op.Grant != "" || op.Adopt != "" || len(op.Milestones) > 0 {
		return errf("E_BAD_ARG", "send a request with one merge effect, without grant, adopt or work milestones",
			"merge must be the request's only effect")
	}
	m := op.Merge
	if m.From == "" || m.Into == "" || m.From == m.Into || !strings.HasPrefix(m.Fence, "sha256:") || len(m.Fence) != 71 {
		return errf("E_BAD_ARG", "get a fresh merge_agents(dry_run:true) preview and copy its ordered IDs and fence",
			"invalid merge request")
	}
	return boundStrings(lim.MaxIDBytes, "merge", []string{m.From, m.Into, m.Fence})
}

// moveOwnedMailRecord is shared with adoption. Selection belongs to each verb:
// adoption leaves settled history, while a merge permanently retires its source.
func (s *State) moveOwnedMailRecord(m *Message, from, into string) {
	m.AdoptedFrom, m.AdoptedAt = from, s.Serial+1
	m.To = into
}

// mergeSupported does not allocate a serial: an approval and its effect are ONE
// transition. Its caller has recorded the authority and checked current policy.
// Historical merges never enter here.
func (s *State) mergeSupported(fromID, intoID string, actor *MergeActor, now time.Time) (Result, []Event) {
	from, into := s.Agents[fromID], s.Agents[intoID]
	moved := 0
	for _, n := range sortedMessageKeys(s.Messages) {
		m := s.Messages[n]
		if s.mergeOwnsMail(fromID, m) {
			s.moveOwnedMailRecord(m, fromID, intoID)
			moved++
		}
		if s.mergeRewritesSender(fromID, m) {
			m.From, m.MergedSentAt = intoID, s.Serial+1
		}
	}
	claims, spaces := s.moveSupportedClaims(fromID, intoID), s.moveSupportedMemberships(fromID, intoID)
	s.moveMergeCredentials(fromID, intoID, actor)
	from.Status, from.Token, from.MergedInto = StatusClosed, "", intoID
	from.MergedBy, from.MergedAt = actor, now
	into.MergedMail = true // recovery must remain nonce-based after mail retention
	evs := []Event{{Type: "agent.merged", Agent: intoID, Data: map[string]any{
		"from": fromID, "by": actor.ID, "messages": moved, "claims": claims, "spaces": spaces,
	}}}
	evs = append(evs, s.movedMailEvents(into, from, true)...)
	return Result{"ok": true, "from": fromID, "into": intoID, "messages": moved, "claims": claims, "spaces": spaces}, evs
}

func (s *State) moveSupportedClaims(from, into string) int {
	n := 0
	for _, c := range s.Claims {
		if c.Agent == from {
			c.Agent = into
			n++
		}
	}
	return n
}

func (s *State) moveSupportedMemberships(from, into string) int {
	n := 0
	for _, id := range sortedKeys(s.Spaces) {
		ch := s.Spaces[id]
		if !mergeSpaceReferences(ch, from) {
			continue
		}
		n++
		if mem := ch.Members[from]; mem != nil {
			if ch.Members[into] == nil {
				mem.Agent = into
				ch.Members[into] = mem
			}
			delete(ch.Members, from)
		}
		if ch.Owner == from {
			ch.Owner = into
		}
		if ch.Subs[from] {
			ch.Subs[into] = true
			delete(ch.Subs, from)
		}
		if ch.Declined[from] {
			ch.Declined[into] = true
			delete(ch.Declined, from)
		}
		mergePendingMembership(ch, from, into)
	}
	return n
}

func mergePendingMembership(ch *Space, from, into string) {
	seenInto := ch.Members[into] != nil || ch.Owner == into
	out := ch.Queue[:0]
	for _, id := range ch.Queue {
		if id == from {
			id = into
		}
		if id == into {
			if seenInto {
				continue
			}
			seenInto = true
		}
		out = append(out, id)
	}
	ch.Queue = out
	if mem := ch.Pending[from]; mem != nil && ch.Pending[into] == nil && ch.Members[into] == nil {
		mem.Agent = into
		ch.Pending[into] = mem
	}
	delete(ch.Pending, from)
	if ch.Members[into] != nil || ch.Owner == into {
		delete(ch.Pending, into)
	}
}

// Validate a recorded merge decision at final ingress. Initial admission precedes
// authentication. Historical ops are never revalidated on replay.
func (s *State) admitMergeActor(op *Op) error {
	// Initial admission precedes engine authentication; final admission sees
	// the recorded decision. A caller can never supply this field through MCP.
	if op.MergeActor == nil {
		return nil
	}
	a := op.MergeActor
	if a.ID == "" || !a.Evidence.Known || a.Evidence.Live {
		return errf("E_NOT_PERMITTED", "use the authenticated merge entry point",
			"merge decision lacks authority or evidence")
	}
	return nil
}

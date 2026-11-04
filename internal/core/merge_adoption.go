// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package core

import (
	"fmt"
	"slices"
	"time"
)

// PendingMergeAdoptions selects evidence for the writer before any sender is
// rewritten. A request originally sent by the source must not become a match
// merely because merging later makes its From equal the survivor.
func (s *State) PendingMergeAdoptions(fromID, intoID string) []uint64 {
	from, into := s.Agents[fromID], s.Agents[intoID]
	if from == nil || into == nil {
		return nil
	}
	var serials []uint64
	for _, serial := range sortedMessageKeys(s.Messages) {
		m := s.Messages[serial]
		if m.Type == MsgRequest && m.Adopt == fromID && m.From == intoID && m.Grant == "" && !m.RetiresIdentity() &&
			!m.Terminal() && m.MergedSentAt == 0 &&
			m.Serial >= from.CreatedSerial && m.Serial >= into.CreatedSerial {
			serials = append(serials, serial)
		}
	}
	return serials
}

func (s *State) admitMergeAdoptions(op *Op) error {
	if len(op.MergeAdoptions) == 0 {
		return nil
	}
	from, into := op.To, op.MergeInto
	if op.Kind == OpRespond && op.Disposition == "approve" {
		if m := s.Messages[op.MsgSerial]; m != nil && m.Merge != nil {
			from, into = m.Merge.From, m.Merge.Into
		} else {
			from, into = "", ""
		}
	} else if op.Kind != OpMergeAgents {
		from, into = "", ""
	}
	if op.MergeActor == nil || from == "" || into == "" ||
		!slices.Equal(op.MergeAdoptions, s.PendingMergeAdoptions(from, into)) {
		return errf("E_BAD_MERGE", "use the authenticated merge entry point with a fresh preview",
			"merge adoption closure does not match the outstanding requests")
	}
	return nil
}

// Fold the exact decision recorded by the writer, never reselect at replay.
// The merge and these outcomes share their caller's one finish/ledger append.
func (s *State) resolveMergeAdoptions(op *Op, from, into string, now time.Time) ([]Event, error) {
	if err := s.checkMergeAdoptionRecords(op); err != nil {
		return nil, err
	}
	var events []Event
	for _, serial := range op.MergeAdoptions {
		m := s.Messages[serial]
		m.State, m.Consumed = MsgStateApproved, true
		m.Response = fmt.Sprintf("fulfilled by merge_agents from %s into %s, by %s at serial %d; "+
			"nothing remains to approve or close",
			from, into, op.MergeActor.ID, s.Serial+1)
		m.RespondedAt, m.TerminalAt = s.Serial+1, now
		events = append(events, Event{
			Type: "message.approved", Agent: op.MergeActor.ID, To: m.From,
			Data: map[string]any{
				"msg_serial": serial, "merged_from": from, "merged_into": into,
				"by": op.MergeActor.ID, "via": "merge_agents",
			},
		})
	}
	return events, nil
}

// Recorded-reference integrity, not a new policy decision. Check before ANY
// mutation as well as at the resolver: a damaged snapshot must fail replay
// deterministically, never panic, silently skip, or partially apply the merge.
// Historical operations have no recorded selection and retain their fold.
func (s *State) checkMergeAdoptionRecords(op *Op) error {
	for _, serial := range op.MergeAdoptions {
		m := s.Messages[serial]
		if m == nil || m.Terminal() {
			return errf("E_MERGE_RECORD",
				"restore a verified snapshot or replay the complete ledger; do not skip the recorded merge",
				"merge op serial %d references missing or already terminal adoption request %d", s.Serial+1, serial)
		}
	}
	return nil
}

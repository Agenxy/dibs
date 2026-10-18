// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package core

import "sort"

// Ref matching: the DECLARED half of the matcher.
//
// Separated from space.go because these functions answer one question: what
// did two agents write down that names the same thing, and because that is the
// question the scorer cannot answer. A ref is a fact an agent typed; a score is
// a resemblance a model computed. When they disagree the ref should win, and the
// bug that prompted this split was the ref never getting to speak: a space
// with no scorer footprint was discarded before its refs were ever read.

// sharedRefsWith compares each member's refs in that member's repository.
// A space can have members from several projects; a union loses provenance.
func (s *State) sharedRefsWith(ch *Space, mine map[string]bool, self string) ([]string, []string) {
	confirmed, unknown := map[string]bool{}, map[string]bool{}
	for id := range ch.Members {
		member := s.Agents[id]
		if member == nil {
			continue
		}
		for _, slot := range member.Slots {
			shared := sharedRefs(mine, s.validatedRefs(id, slot.Refs))
			yes, maybe := scopeSharedRefs(s.Agents[self], member, shared)
			for _, ref := range yes {
				confirmed[ref] = true
			}
			for _, ref := range maybe {
				unknown[ref] = true
			}
		}
	}
	var yes, maybe []string
	for ref := range confirmed {
		yes = append(yes, ref)
		delete(unknown, ref)
	}
	for ref := range unknown {
		maybe = append(maybe, ref)
	}
	sort.Strings(yes)
	sort.Strings(maybe)
	return yes, maybe
}

// unmatchable reports whether a space offers nothing to compare against.
//
// Refs are checked BEFORE a footprint is required, and the ordering is the whole
// point. The caller-side guard already says declared facts must match even when
// the scorer produced no footprint; that rule was applied only to the caller. A
// space whose own opener predicted no files was discarded two lines before
// sharedRefsWith ran, so two agents declaring the same issue:42: same
// repository, same activity: opened two spaces. That is the case an
// identifying ref exists to solve, failing exactly when the scorer had no
// opinion, which is when a hand-written fact matters most.
//
// Nothing here inflates a match: a footprintless space scores 0 from jaccard
// and survives only on the shared ref, which worthless() then judges on its own
// merits.
func unmatchable(fp []PredFile, sharedRefs []string) bool {
	return len(fp) == 0 && len(sharedRefs) == 0
}

// identifying filters shared refs down to the ones that name something.
func identifying(refs []string) []string {
	var out []string
	for _, r := range refs {
		if identifyingRef(r) {
			out = append(out, r)
		}
	}
	return out
}

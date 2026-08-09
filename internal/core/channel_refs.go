package core

import "sort"

// Ref matching: the DECLARED half of the matcher.
//
// Separated from channel.go because these functions answer one question — what
// did two agents write down that names the same thing — and because that is the
// question the scorer cannot answer. A ref is a fact an agent typed; a score is
// a resemblance a model computed. When they disagree the ref should win, and the
// bug that prompted this split was the ref never getting to speak: a channel
// with no scorer footprint was discarded before its refs were ever read.

// channelRefs is every objective id declared by the members of a lane.
func (s *State) channelRefs(ch *Channel) map[string]bool {
	out := map[string]bool{}
	for agent := range ch.Members {
		l := s.Lanes[agent]
		if l == nil {
			continue
		}
		for _, slot := range l.Slots {
			for _, r := range s.validatedRefs(agent, slot.Refs) {
				out[r] = true
			}
		}
	}
	return out
}

// sharedRefsWith returns the objective ids both this lane and the declaring
// agent named, sorted. Nil when the agent declared none.
func (s *State) sharedRefsWith(ch *Channel, mine map[string]bool) []string {
	if len(mine) == 0 {
		return nil
	}
	var out []string
	for r := range s.channelRefs(ch) {
		if mine[r] {
			out = append(out, r)
		}
	}
	sort.Strings(out)
	return out
}

// unmatchable reports whether a channel offers nothing to compare against.
//
// Refs are checked BEFORE a footprint is required, and the ordering is the whole
// point. The caller-side guard already says declared facts must match even when
// the scorer produced no footprint; that rule was applied only to the caller. A
// channel whose own opener predicted no files was discarded two lines before
// sharedRefsWith ran, so two agents declaring the same issue:42 — same
// repository, same activity — opened two channels. That is the case an
// identifying ref exists to solve, failing exactly when the scorer had no
// opinion, which is when a hand-written fact matters most.
//
// Nothing here inflates a match: a footprintless channel scores 0 from jaccard
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

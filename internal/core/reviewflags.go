package core

// Flags are advisory review state, not reopened work debt. A report or accept
// clears only its exact milestone: a partial step cannot clear a whole-work
// (milestone zero) flag.
func (m *Message) unresolvedReviewFlags() map[int]bool {
	flags := map[int]bool{}
	for _, p := range m.Progress {
		if p.Review == ReviewFlagged {
			flags[p.Milestone] = true
		} else {
			delete(flags, p.Milestone)
		}
	}
	return flags
}

// HasUnresolvedReviewFlags reads the current review log.
func (m *Message) HasUnresolvedReviewFlags() bool {
	return m.HasUnresolvedReviewFlagsAfter("", 0)
}

// HasUnresolvedReviewFlagsAfter predicts one response for the engine's recorded
// retention decision. The fold uses that decision only if the response succeeds
// and changes state; an invalid or idempotent response cannot change retention.
func (m *Message) HasUnresolvedReviewFlagsAfter(disposition string, milestone int) bool {
	flags := m.unresolvedReviewFlags()
	switch disposition {
	case "flag":
		flags[milestone] = true
	case "progress", "accept":
		delete(flags, milestone)
	}
	return len(flags) != 0
}

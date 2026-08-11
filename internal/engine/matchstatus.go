package engine

import (
	"sync"
	"time"
)

// MatchPhase says why nothing happened.
//
// Silence is the worst answer a coordination service can give. `set_slot`
// returned exactly `{"ok":true,"slot_id":"s1"}` whether:
//
//   - matching was never configured,
//   - the repository was still being indexed,
//   - the scorer had degraded to the built-in tier,
//   - or it ran fine and genuinely found no overlapping work.
//
// Four unrelated situations, one identical reply. An agent cannot tell which,
// a human cannot tell which, and the only way to find out was to read the
// daemon's log, which agents cannot do. That cost an hour of guessing during
// development, on a codebase whose author wrote the thing.
//
// So every declaration now carries WHY. The rule this encodes, and it is worth
// stating because it is easy to lose: a feature that is off must not look
// identical to a feature that is working and found nothing.
type MatchPhase string

const (
	// MatchOff means no repository configured. Matching is a feature the operator
	// has not turned on, which is a legitimate state and must say so.
	MatchOff MatchPhase = "off"
	// MatchIndexing means configured and still reading. Declarations made now
	// genuinely cannot be matched, and the agent should know to declare again.
	MatchIndexing MatchPhase = "indexing"
	// MatchReady means working. An empty result here means "no overlapping work",
	// which is real information.
	MatchReady MatchPhase = "ready"
	// MatchDegraded means an embedding service was configured and could not be
	// reached, so scoring fell back to the built-in tier. Results are real but
	// weaker, and presenting them as tier 2 would be a lie about how much is
	// known (SPEC-CHANNELS.md §10.5).
	MatchDegraded MatchPhase = "degraded"
	// MatchNoThreshold means scoring works but no join bar was calibrated, so lanes
	// are suggested and never joined. Looks identical to "broken" from outside.
	MatchNoThreshold MatchPhase = "suggest-only"
)

// MatchStatus is the whole truth about work-overlap matching, in one value.
type MatchStatus struct {
	Phase   MatchPhase `json:"phase"`
	Scorer  string     `json:"scorer,omitempty"`
	Files   int        `json:"files,omitempty"`
	Commits int        `json:"commits,omitempty"`
	Repo    string     `json:"repo,omitempty"`
	// Hint is written for whoever is stuck, and names the fix rather than the
	// fault. An agent can act on "declare again in a moment"; it cannot act on
	// "not ready".
	Hint string `json:"hint,omitempty"`
	// Since is when this phase began, so "indexing" that never ends is visible
	// as such rather than looking like a slow but healthy start.
	Since time.Time `json:"since,omitempty"`
}

type matchStatusState struct {
	mu sync.RWMutex
	st MatchStatus
}

// SetMatchStatus records where matching has got to. Called by whatever is
// bringing the scorer up, at every transition: including the failures, which
// are the ones that matter.
func (e *Engine) SetMatchStatus(s MatchStatus) {
	if s.Since.IsZero() {
		s.Since = time.Now()
	}
	e.matchStatus.mu.Lock()
	defer e.matchStatus.mu.Unlock()
	e.matchStatus.st = s
}

// MatchStatus reports it, filling in the hint so callers cannot forget to.
func (e *Engine) MatchStatus() MatchStatus {
	e.matchStatus.mu.RLock()
	st := e.matchStatus.st
	e.matchStatus.mu.RUnlock()

	if st.Phase == "" {
		st.Phase = MatchOff
	}
	if st.Hint == "" {
		st.Hint = matchHint(st)
	}
	return st
}

// matchHint says what to DO. Every one of these names an action, because a
// diagnostic that only names the problem leaves the reader exactly as stuck.
func matchHint(st MatchStatus) string {
	switch st.Phase {
	case MatchOff:
		return "work-overlap matching is not configured; start lanesd with -match-repo <path> " +
			"(or set [match] repo in lanes.toml) to have Lanes tell you who else is doing your work"
	case MatchIndexing:
		waited := time.Since(st.Since).Round(time.Second)
		return "the repository is still being indexed (" + waited.String() + " so far); " +
			"declare again shortly and you will be matched. Nothing is wrong: a first index " +
			"over a large repo, or through an embedding service, takes minutes"
	case MatchDegraded:
		return "the embedding service is unreachable, so matching fell back to the built-in " +
			"scorer: results are real but weaker. Check the service named in the daemon log, " +
			"or run `lanes doctor`"
	case MatchNoThreshold:
		return "no join threshold is set, so lanes are suggested and never joined automatically. " +
			"Run `lanes calibrate` and pass -match-join <value>"
	case MatchReady:
		return ""
	}
	return ""
}

package engine

import (
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// MatchPhase says why nothing happened.
//
// Silence is the worst answer a coordination service can give. `declare`
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
	// MatchNoThreshold means scoring works but no join bar was calibrated, so agents
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
	// Unreadable lists trees the daemon could not read, WITHOUT that being the
	// whole board's problem.
	//
	// One agent registering from a directory macOS will not let the daemon read
	// used to switch matching off for everybody: the failure set the global
	// phase, so a working index for four repositories was replaced by "matching
	// is off" because a fifth agent started somewhere unreadable. Reported by an
	// agent that had lost the feature fleet-wide and traced it correctly.
	//
	// A tree that cannot be read is that AGENT's problem to fix, so it is named
	// here and the phase stays whatever the rest of the board earned.
	Unreadable []string `json:"unreadable,omitempty"`
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
	// Unreadable trees SURVIVE a phase change, because they belong to the trees
	// and not to the phase.
	//
	// This replaced the whole status, and scorer completion calls it with no
	// Unreadable value, so any repository finishing its index silently cleared
	// the record of every OTHER tree the daemon could not read. The operator was
	// told about a permissions problem once and then, a minute later, told
	// nothing, by a success somewhere unrelated. That is the same
	// one-tree-speaks-for-the-board defect this file was written to fix, running
	// the other way. Found by a pre-release review.
	//
	// A caller that genuinely means "none" passes an empty slice, which is
	// distinguishable from not setting the field at all.
	if s.Unreadable == nil {
		// Preserve every OTHER tree's failure, and drop this one's.
		//
		// Preserving the whole list meant a repository whose permissions
		// recovered stayed listed as unreadable until the daemon restarted: no
		// production caller ever sends the empty slice that clears it, so the
		// only thing that could retract the diagnosis was something nothing
		// does. A successful index of THIS tree is proof about this tree, and
		// proof about nothing else, which is exactly the distinction the
		// preserve was added to protect. Raised by the pre-release review, which
		// reproduced the stale entry against the production sequence.
		// Only a SUCCESSFUL index proves a tree readable.
		//
		// Failure paths publish MatchOff with a repository and no Unreadable
		// field, and treating that as recovery erased the record of the very
		// failure being reported: the board could end up ready with nothing
		// saying matching for that tree never came back, which is the
		// "working but found nothing" ambiguity this file exists to remove.
		// Raised by the pre-release review; my tests covered a successful index
		// and an unrelated repository, and not the failed retry.
		if s.Phase == MatchReady {
			s.Unreadable = withoutTree(e.matchStatus.st.Unreadable, s.Repo)
		} else {
			s.Unreadable = e.matchStatus.st.Unreadable
		}
	}
	e.matchStatus.st = s
}

// NoteIndexingTree says a new tree is being indexed, without demoting a board
// that already works.
//
// indexDiscovered set the global phase to `indexing` on every registration, so
// a fleet that had been matching happily for an hour reported itself as still
// starting up whenever a sixteenth agent joined, and any agent that declared in
// that window was told matching was not ready. The same shape as the unreadable
// case below: one tree's progress is not the board's phase.
func (e *Engine) NoteIndexingTree(cwd string) {
	e.matchStatus.mu.Lock()
	defer e.matchStatus.mu.Unlock()
	switch e.matchStatus.st.Phase {
	case MatchReady, MatchDegraded, MatchNoThreshold:
		return // something already works; do not report the board as starting up
	case MatchOff, MatchIndexing:
		// Nothing working to protect, so a new tree's progress IS the board's.
	}
	e.matchStatus.st = MatchStatus{
		Phase: MatchIndexing, Repo: cwd, Since: time.Now(),
		Unreadable: e.matchStatus.st.Unreadable,
	}
}

// NoteUnreadableTree records one tree the daemon cannot read, without changing
// what matching is doing for everybody else.
//
// The distinction is the entire point. "This agent's directory is unreadable"
// and "matching is off" are different facts with different owners: the first is
// one operator granting one permission, the second is a feature nobody has.
// Reporting the first as the second cost a fleet its matching for a day, and
// the hint that came with it sent everyone looking at the wrong thing.
//
// Phase is only forced OFF when nothing at all is indexed, because then the
// two statements happen to coincide.
func (e *Engine) NoteUnreadableTree(cwd, hint string) {
	e.matchStatus.mu.Lock()
	defer e.matchStatus.mu.Unlock()
	for _, seen := range e.matchStatus.st.Unreadable {
		if seen == cwd {
			return
		}
	}
	e.matchStatus.st.Unreadable = append(e.matchStatus.st.Unreadable, cwd)
	// Nothing working to protect: the honest phase is off, and the hint is this
	// tree's, because it is the only evidence there is.
	if e.matchStatus.st.Phase == "" || e.matchStatus.st.Phase == MatchOff ||
		e.matchStatus.st.Phase == MatchIndexing {
		e.matchStatus.st.Phase = MatchOff
		e.matchStatus.st.Hint = hint
		if e.matchStatus.st.Since.IsZero() {
			e.matchStatus.st.Since = time.Now()
		}
	}
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
		return "work-overlap matching is not configured; start dibd with -match-repo <path> " +
			"(or set [match] repo in dibs.toml) to have Dibs tell you who else is doing your work"
	case MatchIndexing:
		waited := time.Since(st.Since).Round(time.Second)
		return "the repository is still being indexed (" + waited.String() + " so far); " +
			"declare again shortly and you will be matched. Nothing is wrong: a first index " +
			"over a large repo, or through an embedding service, takes minutes"
	case MatchDegraded:
		return "the embedding service is unreachable, so matching fell back to the built-in " +
			"scorer: results are real but weaker. Check the service named in the daemon log, " +
			"or run `dibs doctor`"
	case MatchNoThreshold:
		return "no join threshold is set, so agents are suggested and never joined automatically. " +
			"Run `dibs calibrate` and pass -match-join <value>"
	case MatchReady:
		return ""
	}
	return ""
}

// withoutTree drops the trees a successful index has proved readable.
//
// By CONTAINMENT, not by string equality. A failed discovery records the
// agent's working directory, `/repo/subdir`, while a successful index reports
// the repository root it resolved to, `/repo`. Comparing exactly removed a path
// nothing had recorded and left the real entry behind until restart, so the
// operator went on being told about a permissions problem that was fixed. The
// first version of this compared the same path with itself, which is why it
// passed. Raised by the pre-release review.
//
// Reading /repo proves every directory inside it readable; it proves nothing
// about a sibling, so containment runs one way only.
func withoutTree(trees []string, readable string) []string {
	if readable == "" || len(trees) == 0 {
		return trees
	}
	root := filepath.Clean(readable)
	out := trees[:0:0]
	for _, t := range trees {
		if p := filepath.Clean(t); p == root || strings.HasPrefix(p, root+string(filepath.Separator)) {
			continue
		}
		out = append(out, t)
	}
	return out
}

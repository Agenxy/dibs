package core

import (
	"path/filepath"
	"strings"
)

// cleanPath normalizes a claim path: absolute, cleaned, no trailing slash.
func cleanPath(p string) string {
	p = filepath.Clean(p)
	if len(p) > 1 {
		p = strings.TrimSuffix(p, "/")
	}
	return p
}

// pathsOverlap reports whether one path is equal to or an ancestor of the other.
func pathsOverlap(a, b string) bool {
	if a == b {
		return true
	}
	return strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}

// overlapping returns all live claims overlapping path, excluding a lane's own.
func (s *State) overlapping(path, excludeLane string) []*Claim {
	var out []*Claim
	for _, c := range s.Claims {
		if c.Lane != excludeLane && pathsOverlap(c.Path, path) {
			out = append(out, c)
		}
	}
	return out
}

// Signal strengths for an overlap. The distinction is the whole point: two
// agents editing the same file is NORMAL (version control solved that, and
// suppressing it would destroy fleet parallelism). Two agents pursuing the same
// OBJECTIVE is the actual waste: a measured collision burned ~3,900 diff lines
// across three PRs chasing one goal, in files that only incidentally overlapped.
const (
	SignalSameObjective = "same-objective" // strong: probable duplicated effort
	SignalSamePaths     = "same-paths"     // weak: concurrent work, usually fine
	SignalClaim         = "claim"          // someone asked to be left alone here
)

// SlotOverlap is another lane's activity that relates to what you just declared.
// Informational in every case: declaring work never fails.
type SlotOverlap struct {
	Lane   string   `json:"lane"`
	Signal string   `json:"signal"`
	Kind   string   `json:"kind"`           // "slot" | "claim"
	Text   string   `json:"text,omitempty"` // their slot text / claim note
	Refs   []string `json:"refs,omitempty"` // the shared objective ids, if any
	Path   string   `json:"path,omitempty"` // the overlapping path, if any
	Mode   string   `json:"mode,omitempty"` // claims only
}

// Strong reports whether this overlap likely means duplicated effort.
func (o SlotOverlap) Strong() bool { return o.Signal == SignalSameObjective }

// differentProjects reports POSITIVE evidence that two lanes are in different
// repositories. Absence of evidence is not difference: it returns false when it
// cannot tell, so an unidentifiable lane keeps reporting exactly as before.
//
// This gates shared REFS, and only refs. A ref is repository-scoped by nature:
// "issue:42" and "pr:7" name something inside one project and nothing at all
// outside it, so two agents in different repositories declaring "issue:42" are
// not duplicating effort, they are using the same number. Reporting that as
// same-objective, the STRONGEST signal Lanes emits, tells an agent to stop work
// that nothing else is doing. Paths need no such gate: a claim is an absolute
// path, so two projects cannot collide on one.
//
// Three facts are consulted, in order, because two of them leave a case the
// third settles.
//
//  1. The Git common directory. Every linked worktree of one repository shares
//     it, so an equal one is proof of sameness.
//  2. The primary remote. Two clones of one upstream are one project and share
//     its issue numbers; two different remotes are two projects, including two
//     forks, whose issue 42 are genuinely different issues.
//  3. The root commits. A clone whose origin has been removed and a repository
//     created locally with `git init` present identically on the first two
//     facts, and one is the same project while the other is a stranger. Only
//     history separates them, and it does so cleanly: clones share a root
//     commit however the remote has been edited since, and two independent
//     histories never do, because a root commit hashes its own tree, author and
//     timestamp.
//
// Getting to three was not an aesthetic choice. With two, this had to pick which
// case to be wrong about, and it picked the wrong one: an adversarial review
// showed a removed origin silently losing a real same-repository collision,
// which is the expensive failure. Reverting to "warn unless both remotes are
// known and different" would have restored the opposite fault, where one locally
// created repository, which is how most scratch projects start, collided with
// every other project on the machine. Recording history removes the choice.
func differentProjects(a, b *Lane) bool {
	if a == nil || b == nil || a.Agent == nil || b.Agent == nil {
		return false
	}
	x, y := a.Agent, b.Agent
	// One of them is not in a checkout, or never said where it is. That is an
	// absence of evidence, and an unidentified directory could be anywhere,
	// including inside the other's project. Report, and let the human judge.
	if x.RepoDir == "" || y.RepoDir == "" {
		return false
	}
	if x.RepoDir == y.RepoDir {
		return false // one repository, possibly through two linked worktrees
	}
	if x.RepoRemote != "" && y.RepoRemote != "" {
		return x.RepoRemote != y.RepoRemote
	}
	// At least one has no remote to compare. Fall back to shared history.
	if x.RepoRoots == "" || y.RepoRoots == "" {
		return false // an unborn repository has no history to share; say nothing
	}
	return !shareARootCommit(x.RepoRoots, y.RepoRoots)
}

// shareARootCommit reports whether two space-joined root commit lists intersect.
//
// Known limit, in the safe direction: a root commit hashes its tree, author,
// committer, message and timestamps, so two repositories bootstrapped with an
// identical EMPTY initial commit in the same second by the same person really do
// share one. They are then treated as one project and may warn about a shared
// ref that is a coincidence. That costs a warning, not a silence, and it takes a
// deliberate empty commit to arrange; the fixtures in the end-to-end probe hit it
// by accident, which is how it came to be written down.
//
// Intersection rather than equality, because a repository can have more than one
// parentless commit (grafted history, an absorbed subtree), and two clones can
// legitimately disagree about the full set while still plainly being one
// project. Any commit in common is proof enough.
func shareARootCommit(a, b string) bool {
	mine := make(map[string]bool)
	for _, id := range strings.Fields(a) {
		mine[id] = true
	}
	for _, id := range strings.Fields(b) {
		if mine[id] {
			return true
		}
	}
	return false
}

// overlapsFor finds other lanes related to a declaration. Objectives (shared
// refs like "pr:1186", "gate:typos", "issue:1140") are the primary key; paths
// are a weak secondary hint; claims are surfaced because someone asked.
func (s *State) overlapsFor(refs, dirs []string, excludeLane string) []SlotOverlap {
	me := s.Lanes[excludeLane]
	var out []SlotOverlap
	seen := map[string]bool{}
	add := func(o SlotOverlap) {
		k := o.Lane + "\x00" + o.Signal + "\x00" + o.Path + "\x00" + strings.Join(o.Refs, ",")
		if !seen[k] {
			seen[k] = true
			out = append(out, o)
		}
	}
	want := map[string]bool{}
	for _, r := range refs {
		if r = normRef(r); r != "" {
			want[r] = true
		}
	}
	for _, l := range s.Lanes {
		if l.ID == excludeLane || l.Status == StatusClosed || l.Status == StatusArchived {
			continue
		}
		for _, o := range slotOverlaps(me, l, want, dirs) {
			add(o)
		}
	}
	for _, d := range dirs {
		for _, c := range s.overlapping(cleanPath(d), excludeLane) {
			add(SlotOverlap{
				Lane: c.Lane, Signal: SignalClaim, Kind: "claim",
				Text: c.Note, Path: c.Path, Mode: c.Mode,
			})
		}
	}
	sortOverlaps(out)
	return out
}

// slotOverlaps is one other lane's contribution: at most one overlap per slot,
// strongest first. Lifted out of overlapsFor so that the ref gate, the path
// fallback and the collector are each legible on their own.
func slotOverlaps(me, them *Lane, want map[string]bool, dirs []string) []SlotOverlap {
	scoped := !differentProjects(me, them)
	var out []SlotOverlap
	for _, sl := range them.Slots {
		if shared := sharedRefs(want, sl.Refs); len(shared) > 0 && scoped {
			out = append(out, SlotOverlap{
				Lane: them.ID, Signal: SignalSameObjective, Kind: "slot",
				Text: sl.Text, Refs: shared,
			})
			continue // strong signal already reported for this slot
		}
		if p := firstOverlappingPath(dirs, sl.Dirs); p != "" {
			out = append(out, SlotOverlap{
				Lane: them.ID, Signal: SignalSamePaths, Kind: "slot",
				Text: sl.Text, Refs: sl.Refs, Path: p,
			})
		}
	}
	return out
}

// normRef canonicalizes an objective id so "PR #101" and "pr:1186" match.
func normRef(r string) string {
	r = strings.ToLower(strings.TrimSpace(r))
	r = strings.NewReplacer(" ", "", "#", "", "/", ":").Replace(r)
	return strings.Trim(r, ":")
}

func sharedRefs(want map[string]bool, theirs []string) []string {
	var out []string
	for _, r := range theirs {
		if want[normRef(r)] {
			out = append(out, r)
		}
	}
	return out
}

func firstOverlappingPath(mine, theirs []string) string {
	for _, a := range mine {
		for _, b := range theirs {
			if pathsOverlap(cleanPath(a), cleanPath(b)) {
				return b
			}
		}
	}
	return ""
}

func sortOverlaps(o []SlotOverlap) {
	for i := 1; i < len(o); i++ {
		for j := i; j > 0 && overlapLess(o[j], o[j-1]); j-- {
			o[j-1], o[j] = o[j], o[j-1]
		}
	}
}

// overlapLess sorts strong signals first: the agent should read those.
func overlapLess(a, b SlotOverlap) bool {
	if a.Strong() != b.Strong() {
		return a.Strong()
	}
	if a.Lane != b.Lane {
		return a.Lane < b.Lane
	}
	return a.Path < b.Path
}

// releaseClaims drops all claims held by lane, returning the released paths.
func (s *State) releaseClaims(lane string) []string {
	var kept []*Claim
	var released []string
	for _, c := range s.Claims {
		if c.Lane == lane {
			released = append(released, c.Path)
		} else {
			kept = append(kept, c)
		}
	}
	s.Claims = kept
	return released
}

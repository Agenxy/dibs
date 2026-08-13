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

// overlapping returns all live claims overlapping path, excluding an agent's own.
func (s *State) overlapping(path, excludeAgent string) []*Claim {
	var out []*Claim
	for _, c := range s.Claims {
		if c.Agent != excludeAgent && pathsOverlap(c.Path, path) {
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

// SlotOverlap is another agent's activity that relates to what you just declared.
// Informational in every case: declaring work never fails.
type SlotOverlap struct {
	Agent  string   `json:"agent"`
	Signal string   `json:"signal"`
	Kind   string   `json:"kind"`           // "slot" | "claim"
	Text   string   `json:"text,omitempty"` // their slot text / claim note
	Refs   []string `json:"refs,omitempty"` // the shared objective ids, if any
	Path   string   `json:"path,omitempty"` // the overlapping path, if any
	Mode   string   `json:"mode,omitempty"` // claims only
}

// Strong reports whether this overlap likely means duplicated effort.
func (o SlotOverlap) Strong() bool { return o.Signal == SignalSameObjective }

// differentProjects reports POSITIVE evidence that two agents are in different
// repositories. Absence of evidence is not difference: it returns false when it
// cannot tell, so an unidentifiable agent keeps reporting exactly as before.
//
// This gates shared REFS, and only refs. A ref is repository-scoped by nature:
// "issue:42" and "pr:7" name something inside one project and nothing at all
// outside it, so two agents in different repositories declaring "issue:42" are
// not duplicating effort, they are using the same number. Reporting that as
// same-objective, the STRONGEST signal Dibs emits, tells an agent to stop work
// that nothing else is doing. Paths need no such gate: a claim is an absolute
// path, so two projects cannot collide on one.
//
// Three facts are consulted, in order, because two of them leave a case the
// third settles.
//
//  1. **The Git common directory.** Every linked worktree of one repository
//     shares it.
//  2. **The remote**, canonicalised. It is what the user configured as the
//     upstream, so an equal one is the strongest statement available that two
//     checkouts are the same project. It outranks history because history can
//     legitimately differ inside one repository: a `--single-branch` clone of an
//     orphan branch, or a history rewritten by filter-repo, shares no commit
//     with its sibling and is still the same project.
//  3. **Equal root sets.** Clones share their roots however the remote has been
//     renamed, recased or deleted since.
//
// Difference needs positive evidence too, and the absence of any of the above is
// not it:
//
//  4. Root sets that are known on both sides and NOT equal mean different
//     projects, whether they are disjoint or merely overlapping. Overlapping
//     matters: `git subtree add` imports a vendor's whole history, so two
//     unrelated projects that vendored the same dependency each carry their own
//     root plus the shared one. Treating any commit in common as proof fused
//     them and fired the strongest signal Dibs has between strangers.
//
// Anything else is unknown, and unknown warns. That covers a shallow clone,
// which does not have its roots, paired with a remote that cannot be compared
// because one side renamed. A warning somebody dismisses costs less than a
// collision nobody hears about.
//
// Two forks still count as one project: same remote, no; equal roots, yes. That
// is deliberate. A fork normally has its tracker disabled and its references
// name the upstream one, so both agents usually do mean the same issue 42.
func differentProjects(a, b *Agent) bool {
	if a == nil || b == nil || a.Agent == nil || b.Agent == nil {
		return false
	}
	x, y := a.Agent, b.Agent
	if x.RepoDir == "" || y.RepoDir == "" {
		return false // one of them is not in a checkout, or never said where
	}
	if x.RepoDir == y.RepoDir {
		return false // one repository, possibly through two linked worktrees
	}
	if x.RepoRemote != "" && x.RepoRemote == y.RepoRemote {
		return false // configured against the same upstream
	}
	if x.RepoRoots != "" && y.RepoRoots != "" {
		return x.RepoRoots != y.RepoRoots
	}
	return false // no evidence either way
}

// Root sets are compared for EQUALITY, not intersection, and both sides are
// produced by the same sorted, space-joined resolver, so a plain string compare
// is the whole of it. Intersection was tried and was wrong: see the subtree case
// above, where sharing one imported root does not make two projects one.

// overlapsFor finds other agents related to a declaration. Objectives (shared
// refs like "pr:1186", "gate:typos", "issue:1140") are the primary key; paths
// are a weak secondary hint; claims are surfaced because someone asked.
func (s *State) overlapsFor(refs, dirs []string, excludeAgent string) []SlotOverlap {
	me := s.Agents[excludeAgent]
	var out []SlotOverlap
	seen := map[string]bool{}
	add := func(o SlotOverlap) {
		k := o.Agent + "\x00" + o.Signal + "\x00" + o.Path + "\x00" + strings.Join(o.Refs, ",")
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
	for _, l := range s.Agents {
		if l.ID == excludeAgent || l.Status == StatusClosed || l.Status == StatusArchived {
			continue
		}
		for _, o := range slotOverlaps(me, l, want, dirs) {
			add(o)
		}
	}
	for _, d := range dirs {
		for _, c := range s.overlapping(cleanPath(d), excludeAgent) {
			add(SlotOverlap{
				Agent: c.Agent, Signal: SignalClaim, Kind: "claim",
				Text: c.Note, Path: c.Path, Mode: c.Mode,
			})
		}
	}
	sortOverlaps(out)
	return out
}

// slotOverlaps is one other agent's contribution: at most one overlap per slot,
// strongest first. Lifted out of overlapsFor so that the ref gate, the path
// fallback and the collector are each legible on their own.
func slotOverlaps(me, them *Agent, want map[string]bool, dirs []string) []SlotOverlap {
	scoped := !differentProjects(me, them)
	var out []SlotOverlap
	for _, sl := range them.Slots {
		if shared := sharedRefs(want, sl.Refs); len(shared) > 0 && scoped {
			out = append(out, SlotOverlap{
				Agent: them.ID, Signal: SignalSameObjective, Kind: "slot",
				Text: sl.Text, Refs: shared,
			})
			continue // strong signal already reported for this slot
		}
		if p := firstOverlappingPath(dirs, sl.Dirs); p != "" {
			out = append(out, SlotOverlap{
				Agent: them.ID, Signal: SignalSamePaths, Kind: "slot",
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
	if a.Agent != b.Agent {
		return a.Agent < b.Agent
	}
	return a.Path < b.Path
}

// releaseClaims drops all claims held by agent, returning the released paths.
func (s *State) releaseClaims(agent string) []string {
	var kept []*Claim
	var released []string
	for _, c := range s.Claims {
		if c.Agent == agent {
			released = append(released, c.Path)
		} else {
			kept = append(kept, c)
		}
	}
	s.Claims = kept
	return released
}

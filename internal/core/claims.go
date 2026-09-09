package core

import (
	"path/filepath"
	"sort"
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

// repoPathOf names a path the way every checkout of its repository names it:
// relative to the agent's own worktree root. Empty when the agent is not in a
// checkout, or when the path is outside it, both of which are absences of
// evidence rather than answers.
//
// Pure string work on values the server recorded, because the fold cannot call
// Git. The trailing separator matters: without it a root of /a/wt would swallow
// the unrelated /a/wt2, which is the same prefix bug pathsOverlap exists to
// avoid one level down.
func repoPathOf(l *Agent, path string) string {
	if l == nil || l.Agent == nil {
		return ""
	}
	root := l.Agent.RepoRoot
	if root == "" || root == "/" {
		return ""
	}
	if path == root {
		return "."
	}
	rel, found := strings.CutPrefix(path, root+"/")
	if !found {
		return ""
	}
	return rel
}

// sameProject is positive evidence that two agents are in ONE repository.
//
// Not the negation of differentProjects, and the difference is the whole point:
// that one reports evidence of SEPARATION, so its false covers "the same" and
// "no idea" together. A rule that widens what counts as a collision has to rest
// on the first of those alone. The evidence is the same three facts, ranked as
// differentProjects ranks them: a shared Git common directory, then an equal
// configured remote, then equal root commits.
func sameProject(a, b *Agent) bool {
	if a == nil || b == nil || a.Agent == nil || b.Agent == nil {
		return false
	}
	x, y := a.Agent, b.Agent
	switch {
	case x.RepoDir != "" && x.RepoDir == y.RepoDir:
		return true
	case x.RepoRemote != "" && x.RepoRemote == y.RepoRemote:
		return true
	case x.RepoRoots != "" && x.RepoRoots == y.RepoRoots:
		return true
	}
	return false
}

// The two rules an overlap can fire under, reported so a person can act on the
// right one. "Held by api-2 at /Users/kim/src/api" and "held by api-2 in
// another worktree of this repository" send a reader to different places.
const (
	OverlapByPath = "path" // the same absolute path on this filesystem
	OverlapByRepo = "repo" // the same file of the same repository, named differently
)

// claimOverlap decides whether one existing claim covers the path an agent is
// asking about, and by which rule.
//
// TWO RULES, UNIONED, because one absolute path is not one file.
//
// The absolute rule is what shipped, and it is right whenever both agents are
// looking at the same filesystem through the same names. It misses the case
// this repository is worked in every day: two LINKED WORKTREES of one
// repository. /a/wt1/x.go and /a/wt2/x.go are the same tracked file, edited by
// two agents, and the strings do not overlap, so nothing was reported. The
// declare-time signal already scopes by repository and has since it was
// written; claims never did, so the weaker signal knew about the collision and
// the stronger one did not.
//
// The repository rule closes that, and it is the same rule that will carry
// claims across machines when agents stop sharing a filesystem: a clone on
// another computer names the file the same way once its own root is subtracted.
// See docs/NETWORK.md.
//
// Both halves demand positive evidence. Absolute paths must genuinely overlap;
// the repository must be positively the same, and both relative paths must be
// known. Anything short of that is silence, because a claim conflict that fires
// on an absence of evidence teaches agents to ignore claim conflicts.
func (s *State) claimOverlap(me *Agent, path, repoPath string, c *Claim) (rule string, ok bool) {
	if pathsOverlap(c.Path, path) {
		return OverlapByPath, true
	}
	if repoPath == "" || c.RepoPath == "" {
		return "", false
	}
	if !sameProject(me, s.Agents[c.Agent]) {
		return "", false
	}
	if pathsOverlap(c.RepoPath, repoPath) {
		return OverlapByRepo, true
	}
	return "", false
}

// overlapping returns all live claims overlapping path, excluding an agent's own.
func (s *State) overlapping(me *Agent, path, repoPath, excludeAgent string) []*Claim {
	var out []*Claim
	for _, c := range s.Claims {
		if c.Agent == excludeAgent {
			continue
		}
		if _, ok := s.claimOverlap(me, path, repoPath, c); ok {
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
// that nothing else is doing.
//
// Paths need no such gate in this direction: two projects cannot collide on one
// absolute path, so an absolute overlap is never a false alarm about strangers.
// They need the OPPOSITE one, which is sameProject below: two agents in linked
// worktrees of one repository hold different absolute paths for the same file,
// and the absolute rule alone reports nothing at all.
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
		p := cleanPath(d)
		for _, c := range s.overlapping(me, p, repoPathOf(me, p), excludeAgent) {
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
		if p := firstOverlappingPath(me, them, dirs, sl.Dirs); p != "" {
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

// firstOverlappingPath names the other agent's directory that meets one of
// mine, by absolute path or, inside one repository, by the name the file has in
// every checkout of it.
//
// The repository half is the same rule claimOverlap uses and exists for the
// same case: two linked worktrees of one repository hold different absolute
// paths for one tracked file. Without it the weak path signal is silent exactly
// where two agents are editing the same file through separate checkouts, which
// is not an exotic arrangement here: it is how this project reviews its own
// work.
//
// The path REPORTED is always theirs as they declared it, absolute, because
// that is the directory the reader has to go and look at.
func firstOverlappingPath(me, them *Agent, mine, theirs []string) string {
	for _, a := range mine {
		ca := cleanPath(a)
		ra := repoPathOf(me, ca)
		for _, b := range theirs {
			cb := cleanPath(b)
			if pathsOverlap(ca, cb) {
				return b
			}
			if ra == "" || !sameProject(me, them) {
				continue
			}
			if rb := repoPathOf(them, cb); rb != "" && pathsOverlap(ra, rb) {
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

// AgentsIn reports whether any agent that still exists works in this directory,
// whatever its status.
//
// Deliberately NOT ReattachableIn, which answers a different question and was
// briefly used for this one. That one lists rows somebody could reclaim, so it
// excludes ACTIVE agents and rows with no nonce. For "did this hook arrive
// somewhere that coordinates at all", a live agent obviously counts, and it is
// the case that matters most: a directory whose agents are all busy is exactly
// where a hook resolving to nobody is a fault rather than background noise.
func (s *State) AgentsIn(cwd string) bool {
	if cwd == "" {
		return false
	}
	want := cleanPath(cwd)
	for _, l := range s.Agents {
		if l.Gone() || l.Agent == nil {
			continue
		}
		if cleanPath(l.Agent.CWD) == want {
			return true
		}
	}
	return false
}

// ReattachableIn names the idle agents that were working in this directory and
// still hold a nonce, sorted, so the answer is stable.
//
// It exists so the hook path can tell an unresolved session that an identity of
// its own may be waiting, WITHOUT that path re-implementing what "the same
// directory" means. Two implementations of a path rule is a bug this repository
// already carries an open issue about.
//
// Names only. Whether any of them has mail is deliberately not answered here:
// the caller has not proved it is any of these agents, and the reason the hook
// refuses to resolve a supplied session id by directory at all is that an
// earlier build answered a stranger with another agent's messages.
//
// Live agents are excluded: somebody is being them right now, and inviting a
// second session to reattach would fork the identity rather than recover it.
func (s *State) ReattachableIn(cwd string) []string {
	if cwd == "" {
		return nil
	}
	want := cleanPath(cwd)
	var out []string
	for _, l := range s.Agents {
		switch {
		case l.Status == StatusArchived || l.Status == StatusClosed || l.Status == StatusActive:
			continue
		case l.Agent == nil || cleanPath(l.Agent.CWD) != want:
			continue
		case l.Nonce == "":
			continue // nobody can reattach to it, so saying so would be cruel
		}
		out = append(out, l.ID)
	}
	sort.Strings(out)
	return out
}

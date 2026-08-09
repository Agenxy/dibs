package core

import (
	"path"
	"sort"
	"strings"
)

// Evidence is what two agents actually have in common, kept as separate facts.
//
// # Why not one number
//
// Matching used to produce a single score and compare it to a bar. That collapses
// facts of completely different strength into a quantity where enough weak ones
// outweigh a strong one: "same repository, similar prose, two guessed files" could
// reach the same value as "both named pr:1231", and nothing downstream could tell
// the two apart. It also meant the only lever was the bar, so every fix was a
// threshold change, and every threshold change traded one kind of error for the
// other.
//
// Measured, the single score was right about one time in three on real agent
// declarations — while the calibration that set the bar reported healthy numbers,
// because it was scoring a different question (commit message → files) than
// production asks (declaration → will these collide).
//
// So evidence is typed, and the DECISION reads the types rather than a sum. There
// is deliberately no weighted total on this struct: adding one back would restore
// exactly the failure it exists to prevent.
//
// # The three kinds, in descending strength
//
//	IDENTITY   both agents named the same thing that exists — pr:1231, issue:88.
//	           Not a similarity. Two agents holding one identifier are on one piece
//	           of work by construction, and this is the only kind strong enough to
//	           act on without asking anybody.
//
//	SURFACE    both agents declared, or are predicted to touch, the same files or
//	           directories. Real but ambiguous: two agents in one directory may be
//	           colliding or may be doing unrelated things in it, and BOTH appear in
//	           the golden set because both happen.
//
//	SEMANTIC   their prose looks alike to a scorer. The weakest, and the source of
//	           every false positive a live fleet reported — a path-token scorer
//	           turns ordinary shared words (CI, gates, docs, runtime) into
//	           "evidence" neither agent ever wrote.
//
// Provenance is not a fourth kind. Same-repository is a PRECONDITION for the other
// three meaning anything, and different-repository is a veto rather than a weak
// signal.
type Evidence struct {
	// Identity: shared refs that name something. See identifyingRef.
	Identity []string `json:"identity,omitempty"`
	// Contradictory holds identifying refs the two sides gave that DISAGREE —
	// issue:100 against issue:200. Evidence that they are working on different
	// things, and it demotes a duplicate-work claim without silencing a surface
	// collision: two agents can be on different tickets and still edit the same
	// file, which is the most expensive collision there is.
	Contradictory []string `json:"contradictory,omitempty"`
	// Complementary records that the two agents named different ROLES on the same
	// work — implement against review. A shared identifier is then the process
	// working, not a duplication, and telling the reviewer to stand down would be
	// exactly wrong.
	Complementary bool `json:"complementary,omitempty"`
	// Labels: shared refs that name an intention. Reported because they are
	// genuine context, and never acted on: two agents can want main green while
	// having partitioned the repository between them, which is what a real fleet
	// did.
	Labels []string `json:"labels,omitempty"`
	// SurfaceDeclared are paths BOTH agents named themselves.
	SurfaceDeclared []string `json:"surface_declared,omitempty"`
	// SurfaceBroad records that the only shared territory is a COARSE directory —
	// a package root or the repository itself, inside which two agents can work
	// for days without meeting.
	//
	// "Both declared internal/core" is true of half the fleet in a Go project and
	// says almost nothing; "both declared internal/core/queue.go" says a great
	// deal. Reported so the warning can be honest about which it is, rather than
	// spending an agent's attention at the same volume for both.
	SurfaceBroad bool `json:"surface_broad,omitempty"`
	// SurfaceInferred are paths a scorer predicted for both and neither declared.
	// Kept apart from the declared ones because their strength is not comparable
	// and a reader must be able to see which is which.
	SurfaceInferred []string `json:"surface_inferred,omitempty"`
	// Contended are exclusive host resources both agents need — a port, a lock, a
	// GPU. Not repository surface, and not a similarity: two agents that both bind
	// :8080 WILL fail, and the second one gets "address already in use" with no
	// idea why. See Slot.Holds.
	Contended []string `json:"contended,omitempty"`
	// Semantic is the scorer's similarity, retained for ranking candidates. It is
	// NOT a verdict, and nothing in the cascade may act on it alone.
	Semantic float64 `json:"semantic,omitempty"`
	// SameRepo is false only on POSITIVE evidence that the two agents are in
	// different repositories. Unknown is not false: treating it as foreign would
	// disable matching for every client that reports no cwd.
	SameRepo bool `json:"same_repo"`
	// RepoKnown records whether that answer rests on evidence at all.
	//
	// The distinction is load-bearing because IDENTIFIERS ARE REPOSITORY-SCOPED.
	// pr:42 in one project has nothing to do with pr:42 in another, so an exact
	// identifier is only exact once you know both agents are in the same tree.
	// Without that, two agents in unrelated repositories that both write pr:42
	// auto-join — which the first version of this did.
	RepoKnown bool `json:"repo_known"`
}

// Relation is what the evidence supports, and it is deliberately coarser than a
// score. Each value maps to exactly one action, so the action can be read off
// rather than derived from a number nobody can interpret.
type Relation string

const (
	// RelationSameItem means both agents named the same durable work item. Act.
	RelationSameItem Relation = "same_work_item"
	// RelationSameSurface means their DECLARED territory overlaps — the same
	// directory, or one inside the other. Worth telling both, because it is the
	// collision neither can see coming.
	//
	// It does NOT mean they will write the same file, and saying so would be an
	// overclaim: two agents in /repo/.github editing different workflows overlap
	// in territory and never touch each other. Attributable concurrent writes to
	// one FILE would be stronger evidence and Lanes does not observe them.
	RelationSameSurface Relation = "same_surface"
	// RelationContended means they need the same exclusive host resource — a port,
	// a lock, a GPU. Independent of whether the work is related: two unrelated
	// agents binding the same port still break each other, and the failure looks
	// like a bug rather than a collision.
	RelationContended Relation = "contended_resource"
	// RelationPossible means a scorer thinks they look alike. Suggest, quietly.
	RelationPossible Relation = "possible"
	// RelationNone means nothing worth saying.
	RelationNone Relation = "none"
)

// Classify maps evidence to a relation by cascade, strongest first.
//
// A cascade rather than a sum, so that no accumulation of weak evidence can ever
// reach the strength of an exact identifier. Each branch answers a different
// question, and the first one that answers YES wins — later branches cannot
// upgrade or downgrade it.
func (e Evidence) Classify() Relation {
	// Different repositories is a veto, not a weak signal. The only files two
	// unrelated trees share are the ones every project has, so every other kind
	// of evidence is manufactured rather than observed.
	if !e.SameRepo {
		return RelationNone
	}
	// A shared identifier means one piece of work — but not necessarily one JOB.
	// An implementer and a reviewer on pr:1231 are the process working, and
	// classifying that as duplication tells the reviewer to stop reviewing.
	// A contended host resource outranks everything except an exact identity,
	// and unlike every other kind of evidence it does not depend on the agents
	// doing related work at all. Two unrelated agents that both bind :8080 still
	// break each other.
	// An identifier is only exact when both sides are known to be in one
	// repository, because pr:42 THERE is not pr:42 HERE. Unknown provenance
	// downgrades it to something worth showing rather than something to act on.
	if len(e.Identity) > 0 && e.RepoKnown {
		// Complementary does NOT change the relation, and an earlier version that
		// demoted it to "possible" was wrong: it threw away the exact identity in
		// order to avoid the wrong wording. An implementer and a reviewer on
		// pr:1231 ARE on the same work item — that is a fact, and it is exactly
		// what should put them in one lane to talk. What must change is the ACTION
		// and the sentence, not the classification, so Complementary stays on the
		// evidence for the policy to read.
		return RelationSameItem
	}
	if len(e.Contended) > 0 {
		return RelationContended
	}
	// DECLARED surface only. An inferred overlap is a scorer's guess about files
	// neither agent mentioned, and guesses do not get to raise an alarm — they
	// get to suggest, below.
	//
	// Contradictory identifiers do NOT suppress this. Two agents on different
	// tickets editing the same file is not a duplicate, and it is still the
	// collision most worth knowing about — one deleting what the other is editing
	// is in the golden set precisely because differing refs must not veto it.
	if len(e.SurfaceDeclared) > 0 {
		return RelationSameSurface
	}
	// An identifier whose repository provenance is unknown still surfaces: pr:42
	// may well be the same pr:42, and the agent can tell at a glance.
	if len(e.Identity) > 0 || e.Semantic > 0 || len(e.SurfaceInferred) > 0 || len(e.Labels) > 0 {
		return RelationPossible
	}
	return RelationNone
}

// Strongest names the single most important thing the two agents have in common,
// for a one-line explanation. Agents act on the first sentence.
func (e Evidence) Strongest() string {
	return e.qualifyUnknownRepo(e.strongest())
}

// qualifyUnknownRepo says when the evidence below rests on an unverified
// assumption.
//
// SameRepo is false only on POSITIVE evidence of different trees, so "unknown"
// reads as true — deliberately, because treating it as foreign would disable
// matching for every client that reports no cwd. The cost is that the one line an
// agent acts on looked identical whether the shared repository was established or
// merely not disproved.
//
// A reviewer connecting over plain HTTP sent no cwd, and the daemon happened to
// be indexed against a DIFFERENT project. It was shown predicted files from that
// other repository as shared evidence, with same_repo true and repo_known false
// side by side in the payload, and reasonably read the pair as a contradiction.
// Everything under this line — refs, paths, identifiers — is repository-scoped:
// pr:42 in one project has nothing to do with pr:42 in another. So when the
// scoping is unverified, the sentence has to say so rather than leaving it to a
// boolean further down.
func (e Evidence) qualifyUnknownRepo(s string) string {
	if e.RepoKnown || s == "" || s == "different repositories" {
		return s
	}
	return s + " — but neither of you reported a working directory, so it is " +
		"unverified that you are even in the same repository; refs and paths only " +
		"mean the same thing inside one tree"
}

func (e Evidence) strongest() string {
	switch {
	case !e.SameRepo:
		return "different repositories"
	case len(e.Contended) > 0:
		return "you both need " + strings.Join(e.Contended, ", ") +
			" — an exclusive host resource; whoever is second fails"
	case len(e.Identity) > 0 && e.Complementary:
		return "both named " + strings.Join(e.Identity, ", ") +
			", in different roles — coordinate, do not stand down"
	case len(e.Identity) > 0:
		return "both named " + strings.Join(e.Identity, ", ")
	case len(e.SurfaceDeclared) > 0 && e.SurfaceBroad:
		return "you both declared " + strings.Join(e.SurfaceDeclared, ", ") +
			" — but that is a whole area, not a file. Two agents work in it for days " +
			"without meeting, so treat this as awareness rather than a conflict"
	case len(e.SurfaceDeclared) > 0:
		return "you both declared " + strings.Join(e.SurfaceDeclared, ", ") +
			" — overlapping territory, which may or may not be the same files"
	case len(e.SurfaceInferred) > 0:
		return "predicted to touch " + strings.Join(e.SurfaceInferred, ", ") +
			" — predicted, not declared by either of you"
	case len(e.Labels) > 0:
		return "both pursuing " + strings.Join(e.Labels, ", ") +
			" — a shared objective is not a shared task"
	case e.Semantic > 0:
		return "the declarations read similarly"
	}
	return ""
}

// EvidenceBetween compares two live declarations — slot against slot, not
// declaration against a lane's accumulated union.
func EvidenceBetween(
	a, b Slot, aCWD, bCWD, repo string, discount map[string]float64, lens RepoLens,
) Evidence {
	same, known := sameRepo(aCWD, bCWD, repo, lens)
	ev := Evidence{SameRepo: same, RepoKnown: known}
	for _, r := range sharedStrings(a.Refs, b.Refs) {
		if identifyingRef(r) {
			ev.Identity = append(ev.Identity, r)
		} else {
			ev.Labels = append(ev.Labels, r)
		}
	}
	ev.Contended = sharedStrings(a.Holds, b.Holds)
	if Complementary(a.Activity, b.Activity) {
		ev.Complementary = true
	}
	for _, r := range a.Refs {
		if !identifyingRef(r) {
			continue
		}
		for _, s := range b.Refs {
			if identifyingRef(s) && refKind(r) == refKind(s) && r != s {
				ev.Contradictory = append(ev.Contradictory, r+" vs "+s)
			}
		}
	}
	sort.Strings(ev.Contradictory)
	declaredA, declaredB := normDirs(a.Dirs, repo), normDirs(b.Dirs, repo)
	ev.SurfaceDeclared = overlappingPaths(declaredA, declaredB)
	ev.SurfaceBroad = allBroad(ev.SurfaceDeclared)

	score, shared := jaccard(a.Predicted, b.Predicted, discount)
	ev.Semantic = score
	declared := map[string]bool{}
	for _, d := range append(declaredA, declaredB...) {
		declared[d] = true
	}
	for _, f := range shared {
		if !declared[f.Path] {
			ev.SurfaceInferred = append(ev.SurfaceInferred, f.Path)
		}
	}
	sort.Strings(ev.SurfaceInferred)
	return ev
}

// RepoLens answers whether two working directories are one repository, from
// evidence gathered BEFORE the state loop was entered.
//
// Matching runs on the loop, so it must not ask Git anything: a cold
// `git rev-parse` behind a one-second timeout would stall every agent on the
// board while one lane is matched. The engine resolves identities off the loop
// and hands the answers in here as data, which keeps this package a pure
// function of what it was given — the same property that lets the ledger replay.
//
// A nil lens is the honest default, not a degraded one: it means nobody has
// looked, and sameRepo falls back to reasoning about path shape.
type RepoLens interface {
	// SameRepo returns known=false when there is no evidence either way, which
	// is different from evidence that the two are separate.
	SameRepo(aCWD, bCWD string) (same, known bool)
}

// sameRepo reports whether two agents are plausibly in one repository.
//
// Unknown is TRUE, deliberately: the question is "do I have positive evidence
// these are different places", and a client that reports no cwd has told us
// nothing. Treating that as foreign would disable matching for every plain HTTP
// client, which is a worse failure and looks exactly like matching being broken.
// sameRepo answers two questions, because collapsing them was wrong in both
// directions.
//
// The first version returned a bare bool from `under(a,repo) == under(b,repo)`,
// which calls two positively DIFFERENT repositories "the same" whenever neither
// is the configured one — /work/a and /work/b are both not-under /repo, so the
// equality held. With no repo configured it did the opposite, calling /repo/cli
// and /repo/docs different because the strings differ, when they are two
// directories in one checkout.
//
// So: same reports whether they are plausibly together, and known reports whether
// there is any evidence behind that. Only `known && !same` vetoes, and only
// `known && same` lets an identifier act.
func sameRepo(aCWD, bCWD, repo string, lens RepoLens) (same, known bool) {
	if aCWD == "" || bCWD == "" {
		return true, false // nothing observed; do not veto, do not trust identity
	}
	// Ask Git first, through whatever the engine already resolved.
	//
	// Everything below this is inference from the SHAPE of two paths, and shape
	// cannot answer the two questions that matter most. A linked worktree lives
	// outside its checkout and is the same repository; two clones of different
	// projects can sit side by side under one parent and are not. Git knows both;
	// a prefix test guesses at both, and guessed wrong in the direction that
	// silently disables matching for anyone using worktrees.
	//
	// Only a KNOWN answer wins. Unknown falls through rather than vetoing,
	// because "git is not installed" and "these are different repositories" must
	// not produce the same outcome.
	if lens != nil {
		if same, known := lens.SameRepo(aCWD, bCWD); known {
			return same, true
		}
	}
	if repo != "" {
		inA, inB := under(aCWD, repo), under(bCWD, repo)
		if inA && inB {
			return true, true
		}
		if inA != inB {
			return false, true // one is in the indexed tree and the other is not
		}
		// Neither is in the configured repository, so it says nothing about them.
		// Fall through to comparing them with each other.
	}
	if aCWD == bCWD {
		return true, true
	}
	// Different paths outside any known root. One may contain the other — a
	// worktree beneath a checkout — and otherwise we genuinely cannot tell two
	// sibling directories in one repo from two separate clones without asking git.
	if under(aCWD, bCWD) || under(bCWD, aCWD) {
		return true, true
	}
	return true, false
}

func under(path, repo string) bool {
	repo = strings.TrimSuffix(repo, "/")
	return path == repo || strings.HasPrefix(path, repo+"/")
}

// normDirs expresses declared directories the way the index names files, so a
// declared path and a predicted one are comparable at all.
func normDirs(dirs []string, repo string) []string {
	out := make([]string, 0, len(dirs))
	for _, d := range dirs {
		if repo != "" {
			if rel, ok := strings.CutPrefix(d, strings.TrimSuffix(repo, "/")+"/"); ok {
				out = append(out, rel)
				continue
			}
			if d == strings.TrimSuffix(repo, "/") {
				continue // the repo root says nothing about WHERE in it
			}
		}
		out = append(out, strings.TrimPrefix(d, "/"))
	}
	sort.Strings(out)
	return out
}

// refKind is the namespace of a ref, so issue:100 and issue:200 can be seen to
// disagree while issue:100 and pr:900 simply say different things.
func refKind(ref string) string {
	ns, _, _ := strings.Cut(ref, ":")
	return strings.ToLower(ns)
}

// allBroad reports that every shared path is a coarse directory rather than a
// file or a specific subtree.
//
// Depth is the proxy, and it is a proxy: "internal" and "internal/core" are areas
// a fleet shares constantly, while "internal/core/queue.go" is a place two agents
// meet. Anything naming a file is specific regardless of depth, because a file is
// the unit that actually conflicts.
//
// Deliberately does NOT downgrade the relation. Two agents in one package still
// overlap and still deserve to know; what changes is how loudly it is said, since
// an alarm that fires on half the fleet's declarations is one agents learn to
// ignore — and then miss the real one.
func allBroad(shared []string) bool {
	if len(shared) == 0 {
		return false
	}
	for _, p := range shared {
		if strings.Contains(path.Base(p), ".") { // names a file
			return false
		}
		if strings.Count(p, "/") >= 2 { // internal/core/queue → specific enough
			return false
		}
	}
	return true
}

// overlappingPaths matches directories by CONTAINMENT, not string equality.
//
// Exact matching missed the plainest overlap there is: an agent declaring
// `internal` and another declaring `internal/core` are working in the same place,
// and sharedStrings found nothing in common between them. Containment either way
// counts, and the more specific path is reported because it is the one that says
// where.
//
// This is TERRITORY, not a file collision. Two agents inside one directory may be
// editing disjoint children and never meet; the honest claim is that they are in
// the same area and should know about each other.
func overlappingPaths(a, b []string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(p string) {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	for _, x := range a {
		for _, y := range b {
			switch {
			case x == y:
				add(x)
			case strings.HasPrefix(y, x+"/"):
				add(y) // y is inside x; y is the specific one
			case strings.HasPrefix(x, y+"/"):
				add(x)
			}
		}
	}
	sort.Strings(out)
	return out
}

func sharedStrings(a, b []string) []string {
	in := make(map[string]bool, len(a))
	for _, x := range a {
		in[x] = true
	}
	var out []string
	seen := map[string]bool{}
	for _, y := range b {
		if in[y] && !seen[y] {
			seen[y] = true
			out = append(out, y)
		}
	}
	sort.Strings(out)
	return out
}

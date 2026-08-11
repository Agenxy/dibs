package core

import (
	"testing"
)

func slot(text string, dirs, refs []string, pred ...string) Slot {
	return Slot{Text: text, Dirs: dirs, Refs: refs, Predicted: fp(pred...)}
}

// The cascade, case by case. Each is a pair that a single score got wrong or
// could get wrong, and the assertion is on the RELATION rather than a number,
// because the relation is what an action is read off.
func TestClassifyCascade(t *testing.T) {
	const repo = "/repo"
	for _, tc := range []struct {
		name       string
		a, b       Slot
		aCWD, bCWD string
		want       Relation
		why        string
	}{{
		name: "both named the same PR",
		a:    slot("secret scanning gate", []string{"/repo/.github"}, []string{"pr:1191"}),
		b:    slot("reviewing the secret scan PR", []string{"/repo/.github"}, []string{"pr:1191"}),
		aCWD: repo, bCWD: repo,
		want: RelationSameItem,
		why:  "an identifier names a thing; two agents holding it are on one piece of work",
	}, {
		name: "both want main green, in different subsystems",
		a:    slot("greening the CLI gates", []string{"/repo/cli"}, []string{"goal:green-main"}),
		b:    slot("greening the runtime build", []string{"/repo/runtime"}, []string{"goal:green-main"}),
		aCWD: repo, bCWD: repo,
		want: RelationPossible,
		why:  "a shared aspiration is context, not a task: observed verbatim on a live fleet",
	}, {
		name: "same declared directory, different objectives",
		a:    slot("adding a secret-scanning gate", []string{"/repo/.github"}, []string{"pr:1191"}),
		b:    slot("cutting CI wall-clock", []string{"/repo/.github"}, []string{"goal:fast-ci"}),
		aCWD: repo, bCWD: repo,
		want: RelationSameSurface,
		why: "they WILL write the same files even though the work differs, which is the " +
			"collision worth warning about and not a reason to make either stand down",
	}, {
		name: "different repositories, similar words",
		a:    slot("icon and manifest work", []string{"/other/launcher"}, nil, "Justfile"),
		b:    slot("icon and manifest work", []string{"/repo/app"}, nil, "Justfile"),
		aCWD: "/other/site", bCWD: repo,
		want: RelationNone,
		why:  "a different tree is a veto; the only files two unrelated trees share are generic",
	}, {
		name: "nothing but a scorer's opinion",
		a:    slot("queue promotion rewrite", nil, nil, "internal/core/queue.go"),
		b:    slot("queue promotion", nil, nil, "internal/core/queue.go"),
		aCWD: repo, bCWD: repo,
		want: RelationPossible,
		why:  "a terse declaration is common and must still surface, quietly",
	}, {
		name: "one agent reported no cwd",
		a:    slot("auth work", []string{"/repo/auth"}, nil),
		b:    slot("auth work", []string{"/repo/auth"}, nil),
		aCWD: "", bCWD: repo,
		want: RelationSameSurface,
		why:  "unknown is not evidence of separation; a plain HTTP client reports no cwd",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			ev := EvidenceBetween(tc.a, tc.b, tc.aCWD, tc.bCWD, repo, nil, nil)
			if got := ev.Classify(); got != tc.want {
				t.Errorf("relation = %q, want %q\n    %s\n    evidence: %+v",
					got, tc.want, tc.why, ev)
			}
		})
	}
}

// No accumulation of weak evidence may ever reach the strength of an exact one.
// This is the property a weighted sum cannot have, and the reason the cascade
// exists.
func TestWeakEvidenceNeverBecomesIdentity(t *testing.T) {
	const repo = "/repo"
	// Everything weak, all at once: same repo, same directory, same predicted
	// files, near-identical prose, and a shared aspiration.
	weak := EvidenceBetween(
		slot("greening main gates in CI and docs and runtime",
			[]string{"/repo/x"}, []string{"goal:green-main"},
			"a.go", "b.go", "c.go", "d.go"),
		slot("greening main gates in CI and docs and runtime",
			[]string{"/repo/x"}, []string{"goal:green-main"},
			"a.go", "b.go", "c.go", "d.go"),
		repo, repo, repo, nil, nil)
	if got := weak.Classify(); got == RelationSameItem {
		t.Fatal("a pile of weak evidence reached same_work_item; that is the failure " +
			"a weighted sum has and a cascade must not")
	}
	// And one exact identifier, with nothing else at all, outranks it.
	exact := EvidenceBetween(
		slot("", nil, []string{"pr:7"}),
		slot("", nil, []string{"pr:7"}),
		repo, repo, repo, nil, nil)
	if got := exact.Classify(); got != RelationSameItem {
		t.Fatalf("an exact identifier alone must be decisive, got %q", got)
	}
}

// The explanation an agent reads has to name the strongest thing, and say when
// the evidence is a guess. An agent that is told "you are duplicating work" on
// predicted files will stand down real work.
func TestStrongestSaysWhenItIsGuessing(t *testing.T) {
	const repo = "/repo"
	inferred := EvidenceBetween(
		slot("gates", nil, nil, "Justfile"),
		slot("gates", nil, nil, "Justfile"),
		repo, repo, repo, nil, nil)
	if got := inferred.Strongest(); got == "" {
		t.Fatal("no explanation at all")
	} else if !contains(got, "predicted, not declared") {
		t.Errorf("an inferred overlap must be labelled as inferred, got: %s", got)
	}
	labels := EvidenceBetween(
		slot("x", nil, []string{"goal:green-main"}),
		slot("y", nil, []string{"goal:green-main"}),
		repo, repo, repo, nil, nil)
	if got := labels.Strongest(); !contains(got, "not a shared task") {
		t.Errorf("a shared aspiration must be labelled as one, got: %s", got)
	}
}

// The four defects an adversarial review found in the first cascade. Each is a
// concrete pair it misclassified, and each cost is named.
func TestReviewFindings(t *testing.T) {
	const repo = "/repo"
	withAct := func(text string, dirs, refs []string, act string) Slot {
		s := slot(text, dirs, refs)
		s.Activity = act
		return s
	}
	for _, tc := range []struct {
		name string
		a, b Slot
		want Relation
		why  string
	}{{
		// The one that would have been catastrophic.
		name: "implementer and reviewer on one PR",
		a:    withAct("implementing auth token refresh", nil, []string{"pr:1231"}, "implement"),
		b:    withAct("reviewing the auth token refresh changes", nil, []string{"pr:1231"}, "review"),
		want: RelationSameItem,
		why: "they ARE on the same item, and that fact should not be thrown away to " +
			"avoid the wrong wording: an earlier version demoted it to `possible` and " +
			"lost the exact identity. What must change is the ACTION: Complementary " +
			"stays on the evidence, and auto-join reads it.",
	}, {
		name: "two implementers on one PR is still duplication",
		a:    withAct("implementing auth token refresh", nil, []string{"pr:1231"}, "implement"),
		b:    withAct("adding the token refresh path", nil, []string{"pr:1231"}, "implement"),
		want: RelationSameItem,
		why:  "same role, same item: the case auto-join exists for",
	}, {
		name: "a parent directory contains the other",
		a:    slot("core package refactoring", []string{"/repo/internal"}, nil),
		b:    slot("fixing queue logic", []string{"/repo/internal/core"}, nil),
		want: RelationSameSurface,
		why: "guaranteed to meet on internal/core/*. Exact string matching found " +
			"nothing in common and missed the clearest collision there is.",
	}, {
		name: "different tickets, same file",
		a:    slot("fix issue 100", []string{"/repo/internal/core"}, []string{"issue:100"}),
		b:    slot("fix issue 200", []string{"/repo/internal/core"}, []string{"issue:200"}),
		want: RelationSameSurface,
		why: "contradictory identifiers demote the DUPLICATE claim and must not " +
			"silence the surface collision: one agent deleting what another edits " +
			"carries different tickets and is the most expensive collision there is",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			ev := EvidenceBetween(tc.a, tc.b, repo, repo, repo, nil, nil)
			if got := ev.Classify(); got != tc.want {
				t.Errorf("relation = %q, want %q\n    %s\n    evidence %+v", got, tc.want, tc.why, ev)
			}
		})
	}
}

// Unknown activity must never be read as agreement OR as difference.
func TestUnknownActivityIsNotComplementary(t *testing.T) {
	for _, tc := range [][2]string{{"", ""}, {"implement", ""}, {"", "review"}, {"implement", "implement"}, {"weeding", "review"}} {
		if Complementary(tc[0], tc[1]) {
			t.Errorf("Complementary(%q,%q) = true; guessing 'you two are fine' lets real duplication through", tc[0], tc[1])
		}
	}
	if !Complementary("implement", "review") {
		t.Error("implement/review must be recognised as complementary")
	}
}

// Host resources are a whole axis of collision that repository surface cannot
// see, and Dibs exists precisely because these agents share a machine.
func TestContendedHostResources(t *testing.T) {
	const repo = "/repo"
	a, b := slot("integration tests", []string{"/repo/test"}, nil), slot("e2e suite", []string{"/repo/e2e"}, nil)
	a.Holds, b.Holds = []string{"port:8080", "service:postgres"}, []string{"port:8080"}
	ev := EvidenceBetween(a, b, repo, repo, repo, nil, nil)
	if got := ev.Classify(); got != RelationContended {
		t.Fatalf("relation = %q, want %q: two agents binding one port WILL fail, and "+
			"the second gets 'address already in use' with no idea why", got, RelationContended)
	}
	if !contains(ev.Strongest(), "port:8080") {
		t.Errorf("the explanation must name the resource, got: %s", ev.Strongest())
	}
	// Unrelated work is irrelevant here: contention does not care whether the two
	// agents are doing the same thing.
	if len(ev.SurfaceDeclared) != 0 {
		t.Error("fixture drift: these declare different directories on purpose")
	}
}

// The role split lives on the EVIDENCE, so the relation can stay true while the
// action changes. Auto-join reads Complementary; the classification does not.
func TestComplementaryChangesTheActionNotTheFact(t *testing.T) {
	const repo = "/repo"
	impl := slot("implementing token refresh", nil, []string{"pr:1231"})
	impl.Activity = "implement"
	rev := slot("reviewing the token refresh", nil, []string{"pr:1231"})
	rev.Activity = "review"

	ev := EvidenceBetween(impl, rev, repo, repo, repo, nil, nil)
	if ev.Classify() != RelationSameItem {
		t.Errorf("they are on the same item; the relation must say so, got %q", ev.Classify())
	}
	if !ev.Complementary {
		t.Fatal("the role split must be visible to the policy that decides the action")
	}
	if !contains(ev.Strongest(), "do not stand down") {
		t.Errorf("the sentence must not read as an instruction to stop, got: %s", ev.Strongest())
	}
}

// Identifiers are repository-scoped. pr:42 in one project is not pr:42 in
// another, and an unknown location must not be read as agreement.
func TestScopedIdentifiersDoNotCrossRepositories(t *testing.T) {
	a := slot("implement OAuth callback", nil, []string{"pr:42"})
	b := slot("fix README examples", nil, []string{"pr:42"})

	// Positively different trees: a veto.
	if got := EvidenceBetween(a, b, "/repo/x", "/elsewhere", "/repo", nil, nil).Classify(); got != RelationNone {
		t.Errorf("different repositories = %q, want none", got)
	}
	// One side never said where it is: show it, do not act on it.
	unknown := EvidenceBetween(a, b, "", "/work/unrelated", "/repo", nil, nil)
	if got := unknown.Classify(); got == RelationSameItem {
		t.Error("an unlocated pr:42 auto-joined two agents; identifiers are repo-scoped")
	}
	if got := unknown.Classify(); got != RelationPossible {
		t.Errorf("it should still SURFACE, got %q", got)
	}
}

// Two directories in one checkout are not two repositories, and two unrelated
// checkouts are not one. The first version of sameRepo got both backwards.
func TestRepoIdentityBothDirections(t *testing.T) {
	for _, tc := range []struct {
		name, a, b, repo    string
		wantSame, wantKnown bool
	}{
		{"both inside the indexed repo", "/repo/cli", "/repo/docs", "/repo", true, true},
		{"one inside, one outside", "/repo/cli", "/elsewhere", "/repo", false, true},
		{"neither inside, and unrelated", "/work/a", "/work/b", "/repo", true, false},
		{"no repo configured, same checkout", "/repo/cli", "/repo/cli", "", true, true},
		{"a worktree beneath the other", "/repo", "/repo/.worktrees/x", "", true, true},
		{"one side silent", "", "/repo/cli", "/repo", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			same, known := sameRepo(tc.a, tc.b, tc.repo, nil)
			if same != tc.wantSame || known != tc.wantKnown {
				t.Errorf("sameRepo(%q,%q,%q) = (%v,%v), want (%v,%v)",
					tc.a, tc.b, tc.repo, same, known, tc.wantSame, tc.wantKnown)
			}
		})
	}
}

// A shared package root and a shared file are not the same warning.
//
// "Both declared internal/core" is true of half the fleet in a Go project; "both
// declared internal/core/queue.go" is two agents about to meet. The relation is
// the same (they do overlap) but an alarm that fires at full volume on the
// first is one agents learn to ignore, and then they miss the second.
func TestBroadTerritoryIsMarkedAsBroad(t *testing.T) {
	const repo = "/repo"
	for _, tc := range []struct {
		name       string
		aDir, bDir string
		wantBroad  bool
	}{
		{"a whole package", "/repo/internal", "/repo/internal", true},
		{"parent and child, both coarse", "/repo/internal", "/repo/internal/core", true},
		{"a specific subtree", "/repo/internal/core/queue", "/repo/internal/core/queue", false},
		{"an actual file", "/repo/internal/core/queue.go", "/repo/internal/core/queue.go", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ev := EvidenceBetween(
				slot("work", []string{tc.aDir}, nil),
				slot("other work", []string{tc.bDir}, nil),
				repo, repo, repo, nil, nil)
			if ev.Classify() != RelationSameSurface {
				t.Fatalf("relation = %q, want same_surface: they share territory either way",
					ev.Classify())
			}
			if ev.SurfaceBroad != tc.wantBroad {
				t.Errorf("SurfaceBroad = %v, want %v (shared: %v)",
					ev.SurfaceBroad, tc.wantBroad, ev.SurfaceDeclared)
			}
			why := ev.Strongest()
			if tc.wantBroad && !contains(why, "whole area") {
				t.Errorf("a package-wide overlap must be described as one, got: %s", why)
			}
			if !tc.wantBroad && contains(why, "whole area") {
				t.Errorf("a specific overlap must not be softened, got: %s", why)
			}
		})
	}
}

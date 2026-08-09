package core

import (
	"testing"
)

// The release gate for matching: labelled pairs, run through the REAL cascade.
//
// # What was wrong with the first attempt
//
// It lived in internal/overlap, which cannot import core, so it exercised a
// bespoke `declaredOverlap` proxy instead of EvidenceBetween and Classify.
// Passing it said nothing about the code that ships. Worse, three of its tests
// asserted nothing at all: one only logged, one checked recall (so surfacing
// every lane would pass), and the join test explicitly passed when the classifier
// fired on NOTHING. A fixture that cannot fail is not a gate.
//
// It also asked one boolean — "would these collide?" — of pairs that have
// genuinely different answers on different axes. An implementer and a reviewer on
// one PR are a coordination positive and a duplicate-work negative at the same
// time, and a single label has to lie about one of them.
//
// # What this asserts
//
// Per-axis expectations, and separately the ACTION, because the two decisions
// have opposite costs:
//
//	relation  what is true of the pair
//	autoJoin  whether Lanes may act without asking. False positives here are the
//	          expensive kind: an agent told it is duplicating work stands down.
//
// Every case is drawn from something observed on a live fleet or found by an
// adversarial review, and each names the cost of getting it wrong.
type goldenPair struct {
	name       string
	a, b       Slot
	aCWD, bCWD string
	repo       string
	relation   Relation
	autoJoin   bool
	why        string
}

func decl(text string, dirs, refs []string, activity string, holds ...string) Slot {
	return Slot{Text: text, Dirs: dirs, Refs: refs, Activity: activity, Holds: holds}
}

func goldenPairs() []goldenPair {
	const repo = "/repo"
	return []goldenPair{{
		name: "two implementers on one PR",
		a:    decl("implementing token refresh", nil, []string{"pr:1231"}, "implement"),
		b:    decl("adding the refresh path", nil, []string{"pr:1231"}, "implement"),
		aCWD: repo, bCWD: repo, repo: repo,
		relation: RelationSameItem, autoJoin: true,
		why: "the case auto-join exists for: one item, one role, two agents",
	}, {
		name: "implementer and reviewer on one PR",
		a:    decl("implementing token refresh", nil, []string{"pr:1231"}, "implement"),
		b:    decl("reviewing the token refresh", nil, []string{"pr:1231"}, "review"),
		aCWD: repo, bCWD: repo, repo: repo,
		relation: RelationSameItem, autoJoin: false,
		why: "the same item and NOT a duplicate. Auto-joining a reviewer into a " +
			"duplicate-work lane tells it to stop reviewing — the process working, " +
			"reported as waste",
	}, {
		name: "same PR number, different repositories",
		a:    decl("implement OAuth callback", nil, []string{"pr:42"}, "implement"),
		b:    decl("fix README examples", nil, []string{"pr:42"}, "implement"),
		aCWD: "/repo/x", bCWD: "/elsewhere", repo: repo,
		relation: RelationNone, autoJoin: false,
		why: "identifiers are repository-scoped; pr:42 there is not pr:42 here",
	}, {
		name: "same PR number, one agent never said where it is",
		a:    decl("implement OAuth callback", nil, []string{"pr:42"}, "implement"),
		b:    decl("fix README examples", nil, []string{"pr:42"}, "implement"),
		aCWD: "", bCWD: "/work/unrelated", repo: repo,
		relation: RelationPossible, autoJoin: false,
		why: "unknown provenance must not be read as agreement, and must not silence " +
			"the match either — show it, do not act on it",
	}, {
		name: "both want main green, in different subsystems",
		a:    decl("greening the CLI gates", []string{"/repo/cli"}, []string{"goal:green-main"}, ""),
		b:    decl("greening the runtime build", []string{"/repo/runtime"}, []string{"goal:green-main"}, ""),
		aCWD: repo, bCWD: repo, repo: repo,
		relation: RelationPossible, autoJoin: false,
		why: "observed verbatim on a live fleet: two lanes that had deliberately " +
			"partitioned the repository between them both declared goal:green-main",
	}, {
		name: "same directory, different objectives",
		a:    decl("secret-scanning gate", []string{"/repo/.github"}, []string{"pr:1191"}, "implement"),
		b:    decl("cutting CI wall-clock", []string{"/repo/.github"}, []string{"goal:fast-ci"}, "implement"),
		aCWD: repo, bCWD: repo, repo: repo,
		relation: RelationSameSurface, autoJoin: false,
		why: "overlapping territory is worth telling both and is not grounds for " +
			"either to stand down; they may well edit different files in there",
	}, {
		name: "a parent directory contains the other",
		a:    decl("core package refactor", []string{"/repo/internal"}, nil, ""),
		b:    decl("fixing queue logic", []string{"/repo/internal/core"}, nil, ""),
		aCWD: repo, bCWD: repo, repo: repo,
		relation: RelationSameSurface, autoJoin: false,
		why: "exact string matching found nothing in common between these",
	}, {
		name: "different tickets, same directory",
		a:    decl("fix issue 100", []string{"/repo/internal/core"}, []string{"issue:100"}, ""),
		b:    decl("fix issue 200", []string{"/repo/internal/core"}, []string{"issue:200"}, ""),
		aCWD: repo, bCWD: repo, repo: repo,
		relation: RelationSameSurface, autoJoin: false,
		why: "contradictory identifiers demote the duplicate claim and must not " +
			"silence the territory warning — one agent deleting what another edits " +
			"carries different tickets",
	}, {
		name: "two agents needing one port",
		a:    decl("integration tests", []string{"/repo/test"}, nil, "test", "port:8080"),
		b:    decl("e2e suite", []string{"/repo/e2e"}, nil, "test", "port:8080"),
		aCWD: repo, bCWD: repo, repo: repo,
		relation: RelationContended, autoJoin: false,
		why: "unrelated work, guaranteed hard failure. The second agent gets " +
			"'address already in use' and no idea why, and no similarity score can " +
			"represent it",
	}, {
		name: "an agent that merely READ a file elsewhere",
		a:    decl("icon manifest as a format reference only", []string{"/other/launcher"}, nil, "investigate"),
		b:    decl("icon and manifest work", []string{"/repo/app/icon"}, nil, "implement"),
		aCWD: "/other/site", bCWD: repo, repo: repo,
		relation: RelationNone, autoJoin: false,
		why: "the declarations are about the same artifact type and share its whole " +
			"vocabulary, so similarity is most confident exactly where it is wrong",
	}, {
		name: "nothing in common at all",
		a:    decl("rewriting the queue promotion path", []string{"/repo/internal/core"}, nil, ""),
		b:    decl("updating the marketing copy", []string{"/repo/www"}, nil, ""),
		aCWD: repo, bCWD: repo, repo: repo,
		relation: RelationNone, autoJoin: false,
		why: "the set must contain pairs that produce silence, or it cannot detect noise",
	}}
}

// autoJoins mirrors the engine's policy so the gate tests the decision that
// ships, not a restatement of the cascade. Kept in one line for exactly that
// reason: if it drifts from engine.shouldAutoJoin, the drift is visible.
func autoJoins(ev Evidence) bool {
	return ev.Classify() == RelationSameItem && !ev.Complementary
}

func TestGoldenRelations(t *testing.T) {
	for _, tc := range goldenPairs() {
		t.Run(tc.name, func(t *testing.T) {
			ev := EvidenceBetween(tc.a, tc.b, tc.aCWD, tc.bCWD, tc.repo, nil, nil)
			if got := ev.Classify(); got != tc.relation {
				t.Errorf("relation = %q, want %q\n    %s\n    %+v", got, tc.relation, tc.why, ev)
			}
			if got := autoJoins(ev); got != tc.autoJoin {
				t.Errorf("autoJoin = %v, want %v\n    %s", got, tc.autoJoin, tc.why)
			}
		})
	}
}

// The gate that matters: not one false automatic join, on the whole set.
//
// Stated separately from the per-case assertions because this is the property
// that must hold as cases are added — a new case may legitimately change a
// relation, and must never add a false join.
func TestNoFalseAutomaticJoins(t *testing.T) {
	var joins, falseJoins int
	for _, tc := range goldenPairs() {
		ev := EvidenceBetween(tc.a, tc.b, tc.aCWD, tc.bCWD, tc.repo, nil, nil)
		if !autoJoins(ev) {
			continue
		}
		joins++
		if !tc.autoJoin {
			falseJoins++
			t.Errorf("FALSE AUTOMATIC JOIN: %s\n    %s\n    %+v", tc.name, tc.why, ev)
		}
	}
	if falseJoins > 0 {
		t.Errorf("%d false join(s): an agent told it is duplicating work stands down", falseJoins)
	}
	// And it must not have gone dead. A classifier that joins nothing has perfect
	// precision and no value, which is the failure the previous join test could
	// not distinguish from success.
	if joins == 0 {
		t.Error("auto-join fired on nothing; that is not precision, it is absence")
	}
}

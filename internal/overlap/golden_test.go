package overlap

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// The env-var variant of this test is deliberately gone.
//
// It indexed against whatever repository CALIB_REPO named and reported precision
// 1.00 / recall 1.00: against pi-mono, whose paths share no vocabulary with these
// declarations. A benchmark whose result depends on which repository happens to be
// lying around is not measuring the classifier, and this one flattered it: the same
// pairs score 0.33 precision on a tree that reproduces the conditions. It also
// tainted a path from the environment into `git -C`, which gosec was right about.
//
// The same set, decided by what the agents DECLARED rather than by what a scorer
// inferred from their prose. This is the comparison that says whether structure
// is worth more than text here.
func TestGoldenSetAgainstDeclaredSignal(t *testing.T) {
	var tp, fp, tn, fn int
	for _, c := range GoldenSet {
		said := declaredOverlap(c.A, c.B)
		switch {
		case said && c.Same:
			tp++
		case said && !c.Same:
			fp++
			t.Logf("FALSE POSITIVE  %s", c.Name)
		case !said && c.Same:
			fn++
			t.Logf("false negative  %s. %s", c.Name, c.Why)
		default:
			tn++
		}
	}
	t.Logf("")
	t.Logf("DECLARED SIGNAL ONLY: refs, then directory overlap")
	t.Logf("  precision %.2f   recall %.2f   (tp=%d fp=%d tn=%d fn=%d)",
		ratio(tp, tp+fp), ratio(tp, tp+fn), tp, fp, tn, fn)
	// Structure must not be worse than text at the thing it is for. If it ever
	// produces a false positive, the rule is wrong, not the fixture.
	if fp > 0 {
		t.Errorf("declared signal produced %d false positive(s): "+
			"an exact-match rule that fires on unrelated work is not exact", fp)
	}
}

// declaredOverlap is the structural decision: what both agents stated, with no
// scorer involved. Refs first because they are objective ids, then directories,
// which are weaker: two agents can share a directory and do unrelated things in
// it, which the golden set contains on purpose.
func declaredOverlap(a, b GoldenDecl) bool {
	for _, r := range a.Refs {
		for _, s := range b.Refs {
			if r == s {
				return true
			}
		}
	}
	// Directory overlap alone is NOT sufficient, and the "same subsystem,
	// genuinely different work" case is why: both agents declare /repo/.github
	// and are doing unrelated things. Requiring refs to agree where both agents
	// supplied them keeps that case negative without losing the case where one
	// side declared nothing.
	if len(a.Refs) > 0 && len(b.Refs) > 0 {
		return false // both stated objectives, and they disagreed
	}
	for _, d := range a.Dirs {
		for _, e := range b.Dirs {
			if d == e {
				return true
			}
		}
	}
	return false
}

func ratio(n, d int) float64 {
	if d == 0 {
		return 0
	}
	return float64(n) / float64(d)
}

// syntheticRepo builds the file tree the golden declarations live in.
//
// The first version of this test indexed against whatever repository CALIB_REPO
// pointed at and scored a perfect 1.00/1.00: against pi-mono, a TypeScript
// monorepo whose paths share no vocabulary with the declarations at all. That is
// not the classifier being good; it is the fixture failing to reproduce the
// conditions of the failure.
//
// The production failure needs BOTH halves: declarations that share ordinary
// words, and a file tree in which those words are also path tokens. So the tree
// ships with the test. It has what every repository has: a Justfile, a CI
// workflow, a generated bundle at the root: plus disjoint subsystems whose names
// appear in the declarations.
func syntheticRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := []string{
		// The repo-wide files that carried the false evidence.
		"Justfile", ".github/workflows/ci.yml", "llms-full.txt", "CMakeLists.txt",
		// Subsystem A: cli, ui, docs, js deps.
		"cli/main.py", "cli/commands/gate.py", "ui/app.ts", "ui/components/panel.ts",
		"docs/index.md", "docs/gates.md", "sdks/js/package.json",
		// Subsystem B: runtime, CI tooling, build farm.
		"runtime/src/main.cpp", "runtime/CMakeLists.txt", "tools/ci/farm.py",
		".github/workflows/pr-gate.yml", ".github/CODEOWNERS",
		// Subsystem C: the internals the terse case is about.
		"internal/core/channel.go", "internal/core/queue.go", "internal/engine/match.go",
	}
	for _, f := range files {
		p := filepath.Join(dir, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("// "+f+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// The index is built from git ls-files, so the tree has to be a repository.
	for _, args := range [][]string{
		{"init", "-q"},
		{"-c", "user.email=t@t", "-c", "user.name=t", "add", "-A"},
		{"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-qm", "corpus"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git unavailable: %v %s", err, out)
		}
	}
	return dir
}

// The same measurement, on a tree that reproduces the production conditions.
func TestGoldenSetOnTheTreeThatBrokeIt(t *testing.T) {
	ctx := context.Background()
	repo := syntheticRepo(t)
	lex, err := NewLexical(ctx, repo, nil)
	if err != nil || lex.Files() == 0 {
		t.Skipf("no index over the synthetic tree: %v", err)
	}
	const bar = 0.064
	var tp, fp, tn, fn int
	for _, c := range GoldenSet {
		pa, _ := lex.Predict(ctx, c.A.Text, 40)
		pb, _ := lex.Predict(ctx, c.B.Text, 40)
		score := Overlap(pa, pb)
		said := score >= bar
		switch {
		case said && c.Same:
			tp++
		case said && !c.Same:
			fp++
			t.Logf("FALSE POSITIVE  %.4f  %s", score, c.Name)
		case !said && c.Same:
			fn++
			t.Logf("false negative  %.4f  %s", score, c.Name)
		default:
			tn++
		}
	}
	prec := ratio(tp, tp+fp)
	t.Logf("")
	t.Logf("TIER 0 on the reproducing tree, bar %.3f", bar)
	t.Logf("  precision %.2f  recall %.2f  (tp=%d fp=%d tn=%d fn=%d)",
		prec, ratio(tp, tp+fn), tp, fp, tn, fn)
	// An assertion, because a test that only logs cannot fail and is therefore
	// not a test. This one pins the PREMISE of the whole redesign: text alone is
	// not good enough to act on. If it ever climbs near 1.0 the premise has
	// changed and this file needs rereading, not silencing.
	if prec >= 0.9 {
		t.Errorf("tier-0 precision %.2f on the reproducing corpus. The redesign assumes "+
			"text-only similarity is weak; if that is no longer true, revisit the "+
			"decision to make it advisory", prec)
	}
	if fp == 0 {
		t.Error("the corpus no longer reproduces the production false positives, so it " +
			"is not measuring what it claims to")
	}
}

// The SHIPPED pipeline is measured in internal/core, against EvidenceBetween and
// Classify. The version that used to sit here rebuilt a lookalike of the engine's
// logic and then asserted RECALL only, so surfacing every lane on the board would
// have passed it, and it did pass while logging three false positives.
//
// The JOIN gate lives in internal/core (TestNoFalseAutomaticJoins), where it can
// exercise EvidenceBetween and Classify directly. The version that used to sit
// here compared a bespoke proxy, and explicitly PASSED when the classifier fired
// on nothing: precision undefined, reported as success.
//
// The aspiration-vs-identifier rule is tested in internal/core against the real
// namespace table (TestGoldenRelations, "both want main green").

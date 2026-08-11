package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInMatchedRepo(t *testing.T) {
	repo := t.TempDir()
	other := t.TempDir()
	sub := filepath.Join(repo, "cli", "k7_cli")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name, cwd, repo string
		want            bool
	}{
		// The report that found this: an agent working in its own project, scored
		// against a repository it had only ever read one file from, auto-joined on
		// the strength of a Justfile and a ci.yml.
		{"different repo is not a match", other, repo, false},
		{"the repo itself", repo, repo, true},
		{"a subdirectory of the repo", sub, repo, true},

		// Not knowing is not evidence. A plain HTTP client or an older harness
		// reports no cwd, and treating that as foreign would silently disable
		// auto-join for every client that does not report one: a worse failure
		// than the one being fixed, and indistinguishable from matching being
		// broken.
		{"no cwd reported", "", repo, true},
		{"no repo configured", other, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := inMatchedRepo(tc.cwd, tc.repo); got != tc.want {
				t.Errorf("inMatchedRepo(%q, %q) = %v, want %v", tc.cwd, tc.repo, got, tc.want)
			}
		})
	}
}

// A sibling directory must not read as living inside the repo. Prefix matching
// without a separator makes /srv/repo-other look like a subdirectory of
// /srv/repo, which would silently re-open the hole this closes.
func TestInMatchedRepoRejectsPrefixSiblings(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	sibling := filepath.Join(base, "repo-other")
	for _, d := range []string{repo, sibling} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if inMatchedRepo(sibling, repo) {
		t.Errorf("%q must not count as inside %q", sibling, repo)
	}
}

// The guard existed, was correct, and was never called, which is this
// codebase's most expensive recurring defect: built but unreachable. A unit test
// of the predicate alone would have stayed green through the entire outage.
//
// So assert the wiring: MatchConfig must carry the repo, and the daemon must
// fill it in. Without both, inMatchedRepo is handed "" and gates nothing.
func TestRepoGuardIsActuallyWired(t *testing.T) {
	cfg := MatchConfig{Repo: "/somewhere"}
	if cfg.Repo == "" {
		t.Fatal("MatchConfig must carry the matched repo for the guard to see it")
	}
	// The daemon builds the index from `dir`; that same value has to reach the
	// engine, or every agent looks like it is in the repo.
	if !strings.Contains(liveCode(t, matchConfigLiteral(t)), "Repo:") {
		t.Error("dibd builds MatchConfig without Repo: the guard will never fire")
	}
	if !strings.Contains(liveCode(t, readFile(t, "match.go")), "inMatchedRepo(selfCWD, cfg.Repo)") {
		t.Error("suggestionsFor never consults the guard")
	}
}

// matchConfigLiteral returns just the MatchConfig the daemon hands to SetScorer.
//
// Scoped deliberately. The first version of this test searched the whole file
// for "Repo: dir" and passed with the wiring commented out: partly because a
// comment contains its own code, and partly because `Repo: dir` also appears in
// two MatchStatus literals that have nothing to do with the guard. An assertion
// that can be satisfied by a neighbouring struct is not testing the wiring.
func matchConfigLiteral(t *testing.T) string {
	t.Helper()
	src := readFile(t, "../../cmd/dibd/scorer.go")
	i := strings.Index(src, "engine.MatchConfig{")
	if i < 0 {
		t.Fatal("dibd no longer builds an engine.MatchConfig; this test needs rewriting")
	}
	rest := src[i:]
	end := strings.Index(rest, "})")
	if end < 0 {
		t.Fatal("could not find the end of the MatchConfig literal")
	}
	return rest[:end]
}

// liveCode strips line comments, so a commented-out assignment cannot satisfy a
// test that the assignment exists.
func liveCode(t *testing.T, src string) string {
	t.Helper()
	var b strings.Builder
	for _, line := range strings.Split(src, "\n") {
		code, _, _ := strings.Cut(line, "//")
		b.WriteString(code)
		b.WriteByte('\n')
	}
	return b.String()
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

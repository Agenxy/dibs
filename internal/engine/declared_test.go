package engine

import (
	"testing"

	"github.com/agenxy/lanes/internal/core"
)

// What the agent SAID must outrank what a scorer guessed from its prose.
//
// Tier 0 predicts by matching declaration words against file paths one token at a
// time. Probed against a real repository: "CLI + web UI docs, cross-cutting
// gates" returns packages/coding-agent/src/cli/*: the word "cli" appears in
// those paths, and "runtime C++ forge, CI throughput, gate infrastructure"
// returns .github/workflows/pr-gate.yml, from the word "gate". Neither
// declaration is about those files.
//
// That is where the fleet's false matches came from: two agents in one repository
// wrote declarations sharing ordinary words, those words mapped to overlapping
// path sets, and the overlap came back to them as evidence of the same work. The
// evidence was manufactured by the scorer, not observed from either agent.
//
// Reported as: "Both times I declared dirs=[/home/me/proj/builder, ...]. Both
// times the evidence cited files from a sibling project that I never declared
// and have never written. The most explicit signal an agent gives is outranked
// by where its shell happens to sit."
func TestDeclaredDirsOutrankGuessedFiles(t *testing.T) {
	guessed := []core.PredFile{
		{Path: ".github/workflows/pr-gate.yml", Weight: 0.97},
		{Path: "Justfile", Weight: 0.9},
	}
	got := withDeclaredDirs(guessed, []string{"/repo/cli", "/repo/docs"}, "/repo")

	if len(got) != 4 {
		t.Fatalf("want declared dirs added to the guesses, got %d entries: %+v", len(got), got)
	}
	// Declared first, at full weight: nothing inferred outranks a statement.
	for _, want := range []string{"cli", "docs"} {
		found := false
		for _, f := range got {
			if f.Path == want {
				found = true
				if f.Weight < 1 {
					t.Errorf("declared dir %q entered at weight %v, want 1", want, f.Weight)
				}
			}
		}
		if !found {
			t.Errorf("declared dir %q never reached the footprint", want)
		}
	}
	if got[0].Weight < got[len(got)-1].Weight {
		t.Error("declared entries must precede guessed ones")
	}
}

// An agent that declared nothing is still matched: the guesses are how it gets
// found at all, and how a real overlap turns up in a directory it had not thought
// to name.
func TestNoDeclaredDirsLeavesTheGuessesAlone(t *testing.T) {
	guessed := []core.PredFile{{Path: "a.go", Weight: 0.5}}
	got := withDeclaredDirs(guessed, nil, "/repo")
	if len(got) != 1 || got[0].Path != "a.go" {
		t.Fatalf("guesses must survive untouched: %+v", got)
	}
}

// Declaring the whole repository says nothing about WHERE in it, so it must not
// enter the footprint as though it did.
func TestDeclaringTheWholeRepoAddsNothing(t *testing.T) {
	got := withDeclaredDirs(nil, []string{"/repo"}, "/repo")
	if len(got) != 0 {
		t.Fatalf("the repo root is not a location: %+v", got)
	}
}

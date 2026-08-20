package core

import (
	"testing"
	"time"
)

func fp(paths ...string) []PredFile {
	out := make([]PredFile, 0, len(paths))
	for _, p := range paths {
		out = append(out, PredFile{Path: p, Weight: 1})
	}
	return out
}

// Two agents in the same repository doing genuinely different work must not match
// on the files every project has.
//
// Reported from a live fleet: an agent working on CLI, docs and JS dependencies
// was auto-joined to a runtime/CI agent at 0.196 on evidence that was four-fifths
// repo-root files: runtime/CMakeLists.txt, llms-full.txt, ci.yml, Justfile,
// none of which it had declared and none of which it had written. Its own
// diagnosis: "we match mainly because we are both in the same repo."
func TestUbiquitousFilesDoNotCarryTheMatch(t *testing.T) {
	s := NewState("n1", DefaultLimits())

	// Three agents doing three unrelated things. Every one of them touches the
	// boring shared files, because everything in the repo does.
	//
	// Footprint sizes matter here and the first version of this test got them
	// wrong: with two distinctive files per agent the generic ones were 60% of
	// every footprint, which no real declaration looks like. A real footprint is
	// tens of files with a handful of shared build files among them, so that is
	// what this builds: otherwise the fixture, not the code, decides the result.
	common := []string{"Justfile", ".github/workflows/ci.yml", "llms-full.txt"}
	spread := func(prefix string, n int) []string {
		out := make([]string, 0, n)
		for i := range n {
			out = append(out, prefix+"/f"+itoa(i)+".src")
		}
		return out
	}
	runtimeFiles := append(spread("runtime/src", 10), "runtime/CMakeLists.txt")
	cliFiles := append(spread("cli/k7_cli", 8), spread("docs", 3)...)
	webFiles := spread("studio_gateway/web", 9)

	agent(t, s, "runtime", "runtime C++", append(runtimeFiles, common...))
	agent(t, s, "cli", "CLI and docs", append(cliFiles, common...))
	agent(t, s, "web", "web UI", append(webFiles, common...))

	// An agent declaring CLI work, which also happens to touch the common files.
	decl := fp(append(append([]string{}, cliFiles...), common...)...)
	got := s.MatchAgentsWith("newcomer", decl, nil, 5)
	if len(got) == 0 {
		t.Fatal("expected matches")
	}

	bySpace := map[string]float64{}
	for _, m := range got {
		bySpace[m.Space] = m.Score
	}
	// The right space still wins, and by a clear margin: that is the part the
	// discount must not break.
	if bySpace["cli"] <= bySpace["runtime"] {
		t.Fatalf("the genuinely-matching space must rank first: cli=%.3f runtime=%.3f",
			bySpace["cli"], bySpace["runtime"])
	}
	// And the unrelated agents must not clear a calibrated join bar on shared
	// build files alone. 0.064 is the bar measured on the repository where this
	// was reported.
	const joinBar = 0.064
	if bySpace["runtime"] >= joinBar {
		t.Errorf("unrelated agent still auto-joinable on generic files: runtime=%.3f >= %.3f",
			bySpace["runtime"], joinBar)
	}
	if bySpace["web"] >= joinBar {
		t.Errorf("unrelated agent still auto-joinable on generic files: web=%.3f >= %.3f",
			bySpace["web"], joinBar)
	}
}

// A file that only two agents share is real evidence and must keep its weight.
// The discount is meant to remove noise, not to flatten everything toward zero.
func TestRareSharedFilesKeepTheirWeight(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	agent(t, s, "auth", "auth", []string{"internal/auth/token.go", "Justfile"})
	agent(t, s, "ui", "ui", []string{"web/app.ts", "Justfile"})
	agent(t, s, "docs", "docs", []string{"docs/readme.md", "Justfile"})
	// Declaring exactly the auth agent's distinctive file.
	got := s.MatchAgentsWith("newcomer", fp("internal/auth/token.go", "Justfile"), nil, 5)
	if len(got) == 0 || got[0].Space != "auth" {
		t.Fatalf("a distinctive shared file must dominate: %+v", got)
	}
	if got[0].Score < 0.3 {
		t.Errorf("a genuine match was discounted into noise: %.3f", got[0].Score)
	}
}

// With one agent there is nothing to compare against, so nothing is discounted.
// Discounting on a board of one would make the very first match unreachable.
func TestSingleSpaceIsNotDiscounted(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	agent(t, s, "only", "the first agent", []string{"Justfile", "src/main.go"})
	got := s.MatchAgentsWith("newcomer", fp("Justfile", "src/main.go"), nil, 5)
	if len(got) != 1 || got[0].Score < 0.99 {
		t.Fatalf("an identical footprint on a one-agent board must match fully: %+v", got)
	}
}

// agent builds a space the way production does: a member agent holding the
// declaration, and the space footprint merged from it.
//
// Fixtures used to set Space.Predicted with no members at all. That stopped
// being a valid state when scoring moved from the merged footprint to the
// members' own live declarations: a space with a footprint and nobody in it
// cannot reach the code under test, and five tests kept passing against a code
// path that no longer decided anything.
func agent(t *testing.T, s *State, id, topic string, files []string) {
	t.Helper()
	ch := &Space{ID: id, Topic: topic, Members: map[string]*Membership{}}
	member := id + "-owner"
	if _, _, err := s.Apply(&Op{
		Kind: OpRegister, Name: member, NewToken: "tok-" + member,
	}, time.Now()); err != nil {
		t.Fatal(err)
	}
	s.Agents[member].Slots = map[string]Slot{"s1": {ID: "s1", Text: topic, Predicted: fp(files...)}}
	ch.Members[member] = &Membership{}
	ch.Predicted = fp(files...)
	if s.Spaces == nil {
		s.Spaces = map[string]*Space{}
	}
	s.Spaces[id] = ch
}

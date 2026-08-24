package hygiene

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// AGENTS.md tells a newcomer what `task ci` runs, and the count is a claim.
//
// The number of end-to-end suites is the second figure in this project that is
// stated in prose and changed by code, and unlike the tool count it had no
// guard at all: adding a suite is a Taskfile change that nobody thinks of as a
// documentation change. The line read "4 e2e suites" and the gate ran five the
// moment the wake suite landed, which is exactly the shape the tool count went
// stale in three times.
//
// AGENTS.md says it plainly: when you add a claim a test guards, add the SHAPE
// of the claim too. So this accepts either spelling a writer would reach for,
// digits or the English word, and fails when the prose and the gate disagree in
// either direction. Overcounting is the worse error of the two, because it
// describes coverage that does not exist.
//
// What counts as an e2e suite: a `test:<name>` task in the ci list whose
// commands run a `*_e2e.ts` file. The sidecar contract is named separately in
// that sentence and is not one of these; it runs no browser suite.
func TestDocumentedE2ESuiteCountMatchesTheGate(t *testing.T) {
	root := func(p string) string { return filepath.Join("..", "..", p) }

	tf, err := os.ReadFile(root("Taskfile.yml"))
	if err != nil {
		t.Fatalf("reading Taskfile.yml: %v", err)
	}
	// The tasks `ci` actually depends on, so a suite that exists but is not in
	// the gate is not counted: the sentence describes what `task ci` RUNS.
	//
	// THE ci BLOCK, not the whole file. This matched every `- task: test:*`
	// line anywhere in the Taskfile, so a suite dropped from the gate but still
	// referenced by some other aggregate task went on being counted, and the
	// sentence claiming what `task ci` runs stayed green while it ran one
	// fewer.
	ciBlock := regexp.MustCompile(`(?ms)^  ci:\n(.*?)(?:\n  \S|\z)`).FindStringSubmatch(string(tf))
	if ciBlock == nil {
		t.Fatal("no `ci:` task found in Taskfile.yml, so this check cannot tell " +
			"what the gate runs")
	}
	inCI := map[string]bool{}
	for _, m := range regexp.MustCompile(`-\s+task:\s+(test:[\w:-]+)`).FindAllStringSubmatch(ciBlock[1], -1) {
		inCI[m[1]] = true
	}
	if len(inCI) == 0 {
		t.Fatal("no `- task: test:*` lines found in Taskfile.yml: this check would " +
			"pass against a gate that runs nothing, which is the failure it exists to catch")
	}

	// Which of those run an e2e suite.
	suites := 0
	blocks := regexp.MustCompile(`(?m)^  (test:[\w:-]+):`).FindAllStringSubmatchIndex(string(tf), -1)
	for i, b := range blocks {
		name := string(tf)[b[2]:b[3]]
		end := len(tf)
		if i+1 < len(blocks) {
			end = blocks[i+1][0]
		}
		// THE FILES, not the tasks. This counted a task at most once, so a task
		// running two suites counted as one and the documented number stayed
		// green while the gate ran an extra one. The sentence is about suites;
		// count suites.
		if inCI[name] {
			seen := map[string]bool{}
			for _, m := range regexp.MustCompile(`[\w./-]+_e2e\.ts`).
				FindAllString(string(tf)[b[0]:end], -1) {
				seen[filepath.Base(m)] = true
			}
			suites += len(seen)
		}
	}
	if suites == 0 {
		t.Fatal("no ci task runs a *_e2e.ts file: this check verified nothing")
	}

	agents, err := os.ReadFile(root("AGENTS.md"))
	if err != nil {
		t.Fatalf("reading AGENTS.md: %v", err)
	}
	// Both spellings. A writer reaching for prose writes "four e2e suites", and
	// a guard that knows only digits walks straight past it: that is precisely
	// how "one tool of forty-two" survived for months in this repository.
	words := map[string]int{
		"one": 1, "two": 2, "three": 3, "four": 4, "five": 5,
		"six": 6, "seven": 7, "eight": 8, "nine": 9, "ten": 10,
	}
	claim := regexp.MustCompile(`(?i)(\d+|one|two|three|four|five|six|seven|eight|nine|ten)\s+e2e\s+suites?\b`)
	found := claim.FindAllStringSubmatch(string(agents), -1)
	if len(found) == 0 {
		t.Fatalf("AGENTS.md states no e2e suite count, so this check verified nothing. "+
			"The gate runs %d. Either state it (and this guards it) or delete this test, "+
			"but do not leave a guard watching a sentence that is not there", suites)
	}
	for _, m := range found {
		got, err := strconv.Atoi(strings.ToLower(m[1]))
		if err != nil {
			got = words[strings.ToLower(m[1])]
		}
		if got != suites {
			t.Errorf("AGENTS.md says %q; `task ci` runs %d e2e suites. "+
				"Adding a suite is a Taskfile change and reads as nothing to do with the "+
				"docs, which is how the tool count went stale three times in three "+
				"different spellings", m[0], suites)
		}
	}
}

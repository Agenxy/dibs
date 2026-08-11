package mcp

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
)

// Numbers in the documentation must match the software.
//
// The tool count is the most-quoted figure in this project: it appears in the
// README, SKILLS.md, the CHANGELOG and the architecture notes, and it is the
// kind of claim that rots silently: adding a tool is a one-line change that
// nobody thinks of as a documentation change, and the docs then advertise a
// number that is simply false to every reader.
//
// This has already happened once. The README said "Forty tools" while the server
// published 43, and it survived several reviews because nobody counts. Prose is
// not checkable by reading; it is checkable by counting, so this counts.
//
// Deliberately narrow: it pins the ONE number that is repeated everywhere and
// that a reader can verify in seconds. A test that tried to police every figure
// in every document would be brittle enough that people would delete it.
//
// Counted against what tools/list ADVERTISES, not against everything defined.
// Those parted company when the harness lifecycle hooks stopped being listed:
// 43 exist, an agent is offered 38, and "verify in seconds" means running
// tools/list, so 38 is the number a reader can check and therefore the only one
// the docs may quote as "N tools". This test kept asserting the larger figure and
// so demanded the docs state something no reader could confirm.
func TestDocumentedToolCountMatchesReality(t *testing.T) {
	actual := len(agentTools)
	if len(toolDefs) <= actual {
		t.Fatal("nothing is hidden from tools/list; this test is then pinning the wrong number")
	}
	if actual == 0 {
		t.Fatal("no tools are defined; this check would be vacuous")
	}

	root := func(p string) string { return filepath.Join("..", "..", p) }
	// Singular too: "43 tool descriptions" is the same false claim to a reader,
	// and two of them sat in SKILLS.md precisely because the plural-only pattern
	// walked straight past.
	// The leading class matters. `(\d+)\s+tools?` also matches the tail of a
	// version number. "v1.0 tool table" was read as a claim of "0 tools" and
	// failed this test on a sentence that makes no numeric claim at all. A
	// guard that fires on correct prose gets weakened by whoever hits it next.
	claim := regexp.MustCompile(`(?:^|[^\w.])(\d+)\s+tools?\b`)

	checked := 0
	for _, doc := range []string{
		"README.md", "SKILLS.md", "CHANGELOG.md",
		"docs/ARCHITECTURE.md", "internal/mcp/skills.md",
		// Both carry the count and neither was guarded: SPEC.md states it as a
		// contract, and the tutorial quotes a `lanes doctor` transcript that
		// prints it. A number in a transcript goes stale exactly like a number
		// in a sentence, and is likelier to be believed.
		"SPEC.md", "docs/TUTORIAL.md",
	} {
		body, err := os.ReadFile(root(doc))
		if err != nil {
			// A document that has been renamed or removed should not fail this
			// test, but it must not silently reduce coverage to nothing either,
			// which the count below catches.
			continue
		}
		for _, m := range claim.FindAllStringSubmatch(string(body), -1) {
			n, err := strconv.Atoi(m[1])
			if err != nil {
				continue
			}
			checked++
			if n != actual {
				t.Errorf("%s claims %q, but the server publishes %d tools.\n"+
					"  Adding or removing a tool is a documentation change too: every reader\n"+
					"  who trusts that number is being told something false.", doc, m[0], actual)
			}
		}
	}
	if checked == 0 {
		t.Errorf("no document states a tool count, so this check verified nothing; " +
			"either the claim was removed (fine: delete this test) or the pattern no longer matches")
	}
}

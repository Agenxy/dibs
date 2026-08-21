package mcp

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
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
	// Both orders. "44 tools" and "Tools (44)" are the same claim to a reader,
	// and the second walked past this test while SPEC.md advertised 40 for four
	// releases. That is the third spelling this check has been blind to: the
	// first was a plain stale number, the second was "one tool of forty-two",
	// and each was found by somebody reading rather than by the guard whose
	// whole job it is.
	claim := regexp.MustCompile(`(?:^|[^\w.])(\d+)\s+tools?\b|(?i)tools?\s*\((\d+)\)`)

	// ONE list, read by both checks below.
	//
	// There were two, identical and three lines apart, which is the arrangement
	// this repository is most expensive at: a file added to one and not the
	// other is checked for digits and not for words, and nothing says so. The
	// plugin READMEs were in neither, and one of them advertised 25 while the
	// server published 44. It survived because it ALSO used a shape no pattern
	// here matches, "the tools it found (25)", where the number trails the noun
	// with two words in between. Both halves had to be wrong for it to sit
	// there, which is the usual arrangement. That sentence now reads "the 44
	// tools it found", which this test can see.
	docs := []string{
		"README.md", "SKILLS.md", "CHANGELOG.md",
		"docs/ARCHITECTURE.md", "internal/mcp/skills.md",
		// Both carry the count and neither was guarded: SPEC.md states it as a
		// contract, and the tutorial quotes a `dibs doctor` transcript that
		// prints it. A number in a transcript goes stale exactly like a number
		// in a sentence, and is likelier to be believed.
		"SPEC.md", "docs/TUTORIAL.md",
		// Every shipped plugin doc, because each one tells a reader what they
		// will see on install.
		"plugins/README.md", "plugins/hermes/README.md", "plugins/pi/README.md",
		"plugins/opencode/README.md", "plugins/codex/README.md",
		"plugins/claude-code/README.md", "plugins/claude-desktop/README.md",
		"plugins/chatgpt-desktop/README.md",
	}

	checked := 0
	for _, doc := range docs {
		body, err := os.ReadFile(root(doc))
		if err != nil {
			// A document that has been renamed or removed should not fail this
			// test, but it must not silently reduce coverage to nothing either,
			// which the count below catches.
			continue
		}
		for _, m := range claim.FindAllStringSubmatch(string(body), -1) {
			// Whichever alternation matched: one group is the number, the other
			// is empty.
			digits := m[1]
			if digits == "" {
				digits = m[2]
			}
			n, err := strconv.Atoi(digits)
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

	// A count spelled as a WORD is the same claim and was invisible here.
	//
	// The README carried "one tool of forty-two" while the server published 44,
	// through every run of this test, because the pattern above only reads
	// digits. A guard that can be walked past by writing the number out is a
	// guard against one spelling, not against the claim.
	//
	// Refused rather than counted: "forty-four" would have to be parsed and kept
	// in step with the digits, and the cheaper rule is that this one number is
	// written as a numeral wherever it appears, precisely so that it is checked.
	// Matched per LINE, and in either order, because the claim is written both
	// ways: "forty-two tools" and "one tool of forty-two". The first version of
	// this check only caught the first, so it passed against the very sentence
	// that prompted it, which is worse than not having added it.
	spelled := regexp.MustCompile(`(?i)\b(twenty|thirty|forty|fifty|sixty)([- ]?\w+)?\b`)
	for _, doc := range docs {
		body, err := os.ReadFile(root(doc))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(body), "\n") {
			if !strings.Contains(strings.ToLower(line), "tool") {
				continue
			}
			if m := spelled.FindString(line); m != "" {
				t.Errorf("%s says %q on a line about tools. Write the count as a "+
					"numeral: this test reads digits, so a spelled number is a claim "+
					"nothing checks, which is exactly how \"one tool of forty-two\" "+
					"survived to 44.\n  line: %s", doc, m, strings.TrimSpace(line))
			}
		}
	}
}

// register has two continuity paths and the tool description is the only
// documentation an agent ever reads.
//
// It named `reattached` alone. An integrator writes `if result["reattached"]`,
// tests it the obvious way by starting a second session while the first is
// still running, lands on the `resumed` path instead, sees neither the
// documented key nor an explanation, and concludes identity continuity is
// broken. That is what happened to an operator evaluating v0.0.6, and correct
// behaviour looking broken is the failure this project is otherwise careful
// about.
//
// The rotation is asserted separately because it is the half that loses data:
// a client that caches its token across a reattach is holding a dead one.
func TestRegisterDocumentsBothContinuityPaths(t *testing.T) {
	var desc string
	for _, td := range agentTools {
		if td["name"] == "register" {
			desc, _ = td["description"].(string)
		}
	}
	if desc == "" {
		t.Fatal("no register tool: the probe is not reading the listing")
	}
	for _, want := range []string{"resumed", "reattached"} {
		if !strings.Contains(desc, want) {
			t.Errorf("register's description never names %q: an agent that gets it back "+
				"has no way to know what it means", want)
		}
	}
	if !strings.Contains(desc, "ROTATED") {
		t.Error("register's description does not say the token rotates on reattach: a client " +
			"that cached the old one keeps sending a dead token")
	}
}

// A tool's parameter must not contradict its own description.
//
// `prune`'s description says it is NOT for yourself, because signing off
// invalidates the token prune authenticates with and an authenticated caller is
// awakened before pruning and refused as active. Its `agent` parameter went on
// offering "yours". An agent reads both and gets mutually exclusive
// instructions for the exact sequence the description was rewritten to rule
// out. Found by the pre-release review.
func TestPruneDoesNotOfferWhatItRefuses(t *testing.T) {
	var desc, param string
	for _, td := range agentTools {
		if td["name"] != "prune" {
			continue
		}
		desc, _ = td["description"].(string)
		schema, _ := td["inputSchema"].(map[string]any)
		props, _ := schema["properties"].(map[string]any)
		agent, _ := props["agent"].(map[string]any)
		param, _ = agent["description"].(string)
	}
	if desc == "" || param == "" {
		t.Fatal("prune or its agent parameter is missing: the probe reads neither")
	}
	if !strings.Contains(desc, "NOT for yourself") {
		t.Fatalf("prune no longer says it refuses self-pruning, so this guard is "+
			"measuring nothing: %q", desc)
	}
	if strings.Contains(param, "yours") {
		t.Errorf("prune refuses self-pruning in its description and offers it in its "+
			"agent parameter (%q): an agent reading both is told to make a call that "+
			"cannot succeed", param)
	}
}

// claim_coordinator must not repeat the attribution the daemon's own logs were
// corrected for.
//
// It said the role is for the agent that "started this daemon". Under launchd
// or systemd no agent started anything: the authorisation is being able to read
// coordinator.claim. A tool description is the only documentation an agent
// sees, so this discouraged every eligible caller on a service-managed board,
// which can leave that board with no coordinator at all. Found by the
// pre-release review, after the two log lines saying the same thing were fixed.
func TestClaimCoordinatorDoesNotNameAnAgentThatDoesNotExist(t *testing.T) {
	var desc string
	for _, td := range agentTools {
		if td["name"] == "claim_coordinator" {
			desc, _ = td["description"].(string)
		}
	}
	if desc == "" {
		t.Fatal("no claim_coordinator tool: the probe reads nothing")
	}
	if strings.Contains(desc, "started this daemon") {
		t.Errorf("claim_coordinator is offered to the agent that started the daemon, "+
			"which under a service manager is nobody: %q", desc)
	}
	if !strings.Contains(desc, "coordinator.claim") {
		t.Errorf("claim_coordinator does not name the file that authorises it: %q", desc)
	}
}

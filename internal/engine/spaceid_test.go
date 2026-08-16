package engine

import "testing"

// A space id must never quote the declaration that opened it.
//
// It used to be a slug of the prose. An agent in a private repository followed
// dibs://skills, which tells you to declare richly, and its declaration became a
// permanent board object whose ID carried a hostname, a service-account name,
// internal paths and its employer's CI topology, readable by every agent on the
// machine including ones in unrelated repositories. Their operator caught it.
// There was no redaction path: the only remedy was destroying the space.
//
// The test is not "does the id look nice". It is that private words from the
// declaration do not appear in it, whatever the agent wrote.
func TestASpaceIDNeverQuotesTheDeclaration(t *testing.T) {
	// The shape of the real report: infrastructure nouns a private repo would
	// rather not publish.
	decl := "Repairing the k7 CI control plane on forge-prod-07 using svc-ci-deploy, " +
		"LaunchAgents disabled at /opt/k7/secrets/ci.plist for tenant northwind"
	secrets := []string{"forge", "prod", "svc", "deploy", "launchagents", "secrets", "northwind", "k7", "opt"}

	for _, tc := range []struct {
		name string
		refs []string
		repo string
	}{
		{name: "no refs, no repo"},
		{name: "with a repo", repo: "/Users/someone/Desktop/K7/kassette"},
		{name: "with refs", refs: []string{"issue:42"}, repo: "/Users/someone/Desktop/K7/kassette"},
		{name: "with an unidentifying ref", refs: []string{"goal:green-main"}, repo: "/tmp/x"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := spaceID(decl, tc.refs, tc.repo)
			if id == "" {
				t.Fatal("empty id: the space cannot be opened at all")
			}
			for _, leak := range secrets {
				if contains(id, leak) {
					t.Errorf("id %q contains %q from the declaration: this is a durable, "+
						"board-visible object readable by agents in unrelated repositories", id, leak)
				}
			}
		})
	}
}

// A ref is an identifier the agent chose to publish, so it is the best id: two
// agents on issue:42 land on the same space, which is the entire mechanism.
func TestARefBecomesTheSpaceID(t *testing.T) {
	a := spaceID("some private wording", []string{"issue:42"}, "/tmp/repo")
	b := spaceID("completely different private wording", []string{"issue:42"}, "/other/repo")
	if a != b {
		t.Errorf("two agents on issue:42 got different spaces (%q, %q): they will never meet", a, b)
	}
	if a != "issue-42" {
		t.Errorf("id = %q, want %q", a, "issue-42")
	}
}

// Same wording, same id: bootstrapping must be stable or two agents declaring
// the same thing open two spaces and neither finds the other.
func TestTheSameDeclarationOpensTheSameSpace(t *testing.T) {
	decl := "batch ledger appends so we stop paying an fsync per operation"
	// Bound to variables rather than compared inline: staticcheck reads
	// `f(x) != f(x)` as a tautology, which is exactly the property under test.
	first, again := spaceID(decl, nil, "/x/dibs"), spaceID(decl, nil, "/x/dibs")
	if first != again {
		t.Errorf("the id is not stable for one declaration: %q then %q", first, again)
	}
	if spaceID(decl, nil, "/x/dibs") == spaceID("unrelated work entirely", nil, "/x/dibs") {
		t.Error("two different declarations collided into one space")
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

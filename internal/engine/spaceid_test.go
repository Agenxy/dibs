package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/overlap"
)

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

// A long space name must still disambiguate on retry, through the ENGINE.
//
// core.CleanID caps an id at 64 runes, so once the base reaches that length
// every " 2".." 4" suffix was truncated away: all four attempts addressed the
// same existing space, openFirstSpace returned nil, and the declaration
// succeeded while the space it promises was never opened. Silently.
//
// The first version of this guard rebuilt the corrected trimming expression and
// compared it against itself, so restoring the production regression left it
// green: it tested its own copy of the fix. This drives e.Do the way the
// declaration path does, seeds the collision it has to survive, and looks at
// what actually got opened. Raised by the pre-release review.
func TestALongSpaceNameStillDisambiguates(t *testing.T) {
	// A long id arrives through REFS, not through the declaration: spaceID
	// hashes the prose deliberately (see the test above), so the only route to a
	// name at the 64-rune cap is an identifying ref the agent published, which
	// is what the original comment named: "a ref like `ticket:` plus a long key
	// reaches 64 easily". Driving it any other way tests nothing, because the
	// generic `work-0f45e8` id is eleven runes and never reaches the cap.
	ref := "ticket:" + strings.Repeat("k", 80)
	// The cap is applied by core.CleanID when the op is admitted, not by
	// spaceID, so that is where the setup has to look.
	base := spaceID("some work", []string{ref}, "")
	if n := len([]rune(core.CleanID(base))); n != 64 {
		t.Fatalf("setup: this ref cleans to %d runes, not the 64-rune cap, so a suffix "+
			"would survive and this proves nothing", n)
	}

	st := core.NewState("test", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	token := func(name string) string {
		t.Helper()
		res, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: name, Nonce: "n-" + name})
		if err != nil {
			t.Fatalf("setup: register %s: %v", name, err)
		}
		tok, _ := res["token"].(string)
		if tok == "" {
			t.Fatalf("setup: no token for %s", name)
		}
		// The awareness gate: a space cannot be opened before the board is
		// acknowledged, so this is setup rather than the thing under test.
		if _, err := e.Do(ctx, &core.Op{Kind: core.OpAckBoard, Token: tok}); err != nil {
			t.Fatalf("setup: check_in %s: %v", name, err)
		}
		return tok
	}

	// Three agents declare the same over-long work. openFirstSpace is the
	// production path that retries on collision, so that is what this calls:
	// e.Do alone just returns E_SPACE_EXISTS, which is the condition the retry
	// exists to resolve rather than the behaviour under test.
	seen := map[string]bool{}
	for i, who := range []string{"one", "two", "three"} {
		sug := e.openFirstSpace(ctx, token(who), "some work", []string{ref}, "", overlap.Prediction{}, nil)
		if sug == nil {
			t.Fatalf("agent %d got no space at all: every retry collided with the same "+
				"id, which is the silent failure this guards", i+1)
		}
		if seen[sug.Space] {
			t.Errorf("agent %d was given %q, which an earlier agent already holds: the "+
				"suffix is being truncated away and every retry addresses one space",
				i+1, sug.Space)
		}
		seen[sug.Space] = true
	}
}

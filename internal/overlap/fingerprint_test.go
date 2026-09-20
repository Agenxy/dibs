package overlap

import (
	"context"
	"testing"
)

// The fingerprint identifies the HISTORY an index was mined from, so two
// indexes of one project can be told apart from two views of the same one
// (issue #39). Same history and bounds: same fingerprint. A different window
// over the same history is a different model of it, and says so.
func TestFingerprintIdentifiesTheMinedHistory(t *testing.T) {
	repo := repoRoot(t)
	ctx := context.Background()
	mine := func(n int) string {
		cc, err := MineCoChange(ctx, repo, CoChangeOptions{MaxCommits: n})
		if err != nil {
			t.Skip("git log unavailable:", err)
		}
		return cc.Fingerprint()
	}
	first, again := mine(120), mine(120)
	if first == "" {
		t.Fatal("fingerprint empty for a repository with history")
	}
	if first != again {
		t.Errorf("the same history mined twice fingerprinted %q then %q: two clones "+
			"at the same commit must read as one coordinate system", first, again)
	}
	if narrower := mine(40); narrower == first {
		t.Errorf("a 40-commit window and a 120-commit window fingerprinted the same, %q: "+
			"they are different models of the history and predict differently", first)
	}
}

// And an INDEX's fingerprint identifies the files it scores over, not only
// the history behind it.
//
// Two checkouts at one commit, one with a staged rename, share a history
// and name the same work by two paths: `auth/refresh_token.go` in one,
// `session/refresh_token.go` in the other. The history fingerprint called
// them one coordinate system, so the engine skipped scoring each
// declaration in the other's index, and two agents on the same file
// compared as strangers: the zero issue #39 exists to remove, one working
// tree deep. The daemon and a shipper (Ship) now name an index by history
// AND tracked files, through one function. Round twenty-six of the
// pre-release review.
func TestAnIndexIsFingerprintedByItsFilesAsWellAsItsHistory(t *testing.T) {
	cc := &CoChange{commits: map[string]int{}, pairs: map[string]map[string]int{}, fingerprint: "hist-1"}
	before := NewLexicalFromFiles([]string{"auth/refresh_token.go", "main.go"}, cc)
	renamed := NewLexicalFromFiles([]string{"session/refresh_token.go", "main.go"}, cc)
	same := NewLexicalFromFiles([]string{"main.go", "auth/refresh_token.go"}, cc) // order is not identity
	if before.Fingerprint() == "" || before.Fingerprint() == renamed.Fingerprint() {
		t.Fatalf("an index with a renamed file fingerprinted as the one before the rename (%q): the two "+
			"predict the same work by different paths and would not be scored in each other", before.Fingerprint())
	}
	if before.Fingerprint() != same.Fingerprint() {
		t.Fatalf("the same files in another order fingerprinted differently: %q vs %q", before.Fingerprint(), same.Fingerprint())
	}
	// What a shipper sends is what the daemon would compute for that tree.
	if got := IndexFingerprint("hist-1", []string{"main.go", "auth/refresh_token.go"}); got != before.Fingerprint() {
		t.Fatalf("Ship's fingerprint %q differs from the index's %q for the same tree", got, before.Fingerprint())
	}
	if IndexFingerprint("", nil) != "" {
		t.Fatal("nothing to identify fingerprinted as something")
	}
}

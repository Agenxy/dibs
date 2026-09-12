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

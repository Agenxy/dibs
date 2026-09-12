package overlap

import (
	"context"
	"testing"
)

// An index rebuilt from the records an agent ships predicts what the index
// mined from the checkout predicts. That is the whole claim of issue #19: the
// daemon needs read access to nothing outside its own directory, because the
// agent already has the access, is already inside the repository, and can
// hand over the two bounded things the index is built from.
func TestAnIndexRebuiltFromRecordsPredictsLikeTheMinedOne(t *testing.T) {
	repo := repoRoot(t)
	ctx := context.Background()
	opt := CoChangeOptions{MaxCommits: 300, MaxFilesPerCommit: 25}
	mined, err := MineCoChange(ctx, repo, opt)
	if err != nil {
		t.Skip("git log unavailable:", err)
	}
	local, err := NewLexical(ctx, repo, mined)
	if err != nil {
		t.Skip("git ls-files unavailable:", err)
	}

	// What travels: the tracked files, the records, the fingerprint.
	files, err := TrackedFiles(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	shipped := NewLexicalFromFiles(files, FromRecords(mined.Records(), mined.Fingerprint(), opt))

	if shipped.Files() != local.Files() {
		t.Fatalf("shipped index has %d files, mined has %d", shipped.Files(), local.Files())
	}
	if got, want := shipped.cc.Fingerprint(), mined.Fingerprint(); got != want {
		t.Errorf("fingerprint %q, want the mined %q: two views of one history must read as one", got, want)
	}
	for _, decl := range []string{
		"the ledger's hash chain on replay",
		"work-overlap matching and the co-change scorer",
		"the wake path for an agent that is not running",
	} {
		a, _ := local.Predict(ctx, decl, 20)
		b, _ := shipped.Predict(ctx, decl, 20)
		if len(a.Files) == 0 {
			continue
		}
		if o := Overlap(a, b); o < 0.99 {
			t.Errorf("%q: shipped and mined predictions overlap %.3f, want them to be the "+
				"same index; a subject-less commit is the only difference allowed", decl, o)
		}
	}
}

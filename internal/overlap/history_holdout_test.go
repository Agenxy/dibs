package overlap

import (
	"context"
	"path/filepath"
	"testing"
)

// The history index must beat a PUNITIVE hold-out, not just a fair one.
//
// The shipped hold-out removes the query's own commit, which stops the query
// retrieving itself. A hostile reviewer's next question is the right one: what
// about NEAR-duplicates — reverts, follow-ups, a squashed series, "fix typo"
// twice? On these repositories 51-66% of queries have some other commit sharing
// two or more significant terms, so if that were the whole effect the gain would
// be leakage wearing a hold-out.
//
// So this removes the query's commit AND every commit sharing two or more terms
// with it — deleting roughly half the corpus — and asserts the gain survives.
// It does, smaller: on this evidence the improvement is generalisation, and
// near-duplicates contribute to it rather than being it.
//
// Skipped unless those repositories are present; it measures against real
// history, which cannot be fabricated in a fixture without becoming a fixture.
func TestHistoryGainSurvivesAPunitiveHoldout(t *testing.T) {
	ctx := context.Background()
	// A sibling of the repository rather than one machine's home directory.
	//
	// Deliberately a literal relative path and not an environment variable: the
	// functions under test shell out to git, so a path arriving from the
	// environment makes every one of those call sites a tainted sink and the
	// security linter — correctly — objects. The paths were absolute and
	// personal before, which skipped for everyone but their author; this skips
	// for everyone without one, which is the same outcome without the name.
	for _, repo := range []string{
		filepath.Join("..", "..", "..", "harnesses", "hermes-agent"),
		filepath.Join("..", "..", "..", "harnesses", "opencode"),
		filepath.Join("..", "..", "..", "harnesses", "pi-mono"),
	} {
		cc, err := MineCoChange(ctx, repo, DefaultCoChangeOptions)
		if err != nil {
			t.Skip(err)
		}
		cases, err := SampleCommits(ctx, repo, 60, 25, 5)
		if err != nil {
			t.Skip(err)
		}

		strict := map[string]bool{}
		for _, c := range cases {
			strict[c.Message] = true
			q := map[string]bool{}
			for tk := range tokenize(c.Message) {
				if !actionWords[tk] {
					q[tk] = true
				}
			}
			for _, m := range cc.Messages {
				shared := 0
				for tk := range tokenize(m.Subject) {
					if q[tk] {
						shared++
					}
				}
				if shared >= 2 {
					strict[m.Subject] = true
				}
			}
		}

		noHist, err := newLexical(ctx, repo, nil, nil)
		if err != nil {
			t.Skip(err)
		}
		normal, err := newLexical(ctx, repo, cc, holdSet(cases))
		if err != nil {
			t.Skip(err)
		}
		harsh, err := newLexical(ctx, repo, cc, strict)
		if err != nil {
			t.Skip(err)
		}

		a, _ := Evaluate(ctx, noHist, cases, []int{5})
		b, _ := Evaluate(ctx, normal, cases, []int{5})
		c, _ := Evaluate(ctx, harsh, cases, []int{5})
		t.Logf("%-13s recall@5  none %.3f | held-out %.3f | punitive %.3f  (%d of %d commits removed)",
			repo[len(repo)-12:], a.RecallAt[5], b.RecallAt[5], c.RecallAt[5], len(strict), len(cc.Messages))
		if c.RecallAt[5] <= a.RecallAt[5] {
			t.Errorf("%s: history gave no gain under a punitive hold-out (%.3f vs %.3f) — "+
				"the published improvement would be near-duplicate leakage",
				repo, c.RecallAt[5], a.RecallAt[5])
		}
	}
}

func holdSet(cases []EvalCase) map[string]bool {
	m := map[string]bool{}
	for _, c := range cases {
		m[c.Message] = true
	}
	return m
}

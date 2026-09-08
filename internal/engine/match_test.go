package engine

import (
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// A space that could not be created outranks the status hint.
//
// annotateMatching answers "why is there nobody" and it took the MatchStatus
// hint first, because that hint explains why matching answered weakly. But
// MatchStatus supplies a hint for every non-ready phase, and the DEFAULT phase
// is suggest-only: a board with no join threshold, which is most of them.
//
// So on an ordinary board, an agent whose fallback space could not be created
// was told "no join threshold is set" and nothing else. That sentence is true
// and it is not the news. The news is that nothing matched, no space was
// opened, and there is nowhere for the next agent to find them: the exact
// misreading the matchedNoSpace outcome was added to prevent, reachable only on
// a board configured in a way most are not.
func TestAFailedSpaceIsReportedAheadOfTheMatchingStatus(t *testing.T) {
	// The ordinary board: suggest-only, which always carries a hint.
	st := MatchStatus{Phase: MatchNoThreshold, Hint: "no join threshold is set, so agents are suggested"}
	if st.Hint == "" {
		t.Fatal("setup: the status carries no hint, so there is nothing for the " +
			"failed space to be hidden behind and this test proves nothing")
	}

	res := core.Result{}
	annotateMatching(res, nil, matchedNoSpace, st)
	hint, _ := res["matching_hint"].(string)
	if !strings.Contains(hint, "could not be created") {
		t.Errorf("an agent whose fallback space failed is told %q.\n\nIt is not told "+
			"that there is no space for anybody to find it in, which is the one thing "+
			"it cannot infer and the reason this outcome exists", hint)
	}

	// And the status hint still reaches an agent when nothing failed, or this
	// fix would just be a different sentence going missing.
	res = core.Result{}
	annotateMatching(res, nil, matchedNoOpinion, st)
	if hint, _ := res["matching_hint"].(string); !strings.Contains(hint, "join threshold") {
		t.Errorf("the matching status no longer reaches an agent on an ordinary "+
			"result: %q", hint)
	}
}

package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/overlap"
)

// Every phrasing that asserts absence rather than reporting a measurement.
var claimsOfSolitude = []string{
	"nobody else is", "no one else is", "no agent existed",
	"you are alone", "you have the field", "nobody is working",
}

// mustNotClaimSolitude scans what the text ASSERTS, not which substrings occur
// in it.
//
// The honest wording deliberately contains these phrases inside their own
// negations, "a miss is not proof you are alone" is the sentence we want, so a
// bare substring scan flagged the fix as the bug. That is the third time in this
// codebase a guard has matched a token instead of reading the claim, which is the
// same mistake as the code it polices. Negated clauses are dropped first; what
// remains is what the text actually says.
func mustNotClaimSolitude(t *testing.T, where, s string) {
	t.Helper()
	var asserted []string
	for _, clause := range strings.FieldsFunc(strings.ToLower(s),
		func(r rune) bool { return r == '.' || r == ';' || r == ':' }) {
		if strings.Contains(clause, "not proof") || strings.Contains(clause, "is not") {
			continue // a denial of the claim, which is what we want it to say
		}
		asserted = append(asserted, clause)
	}
	low := strings.Join(asserted, " ")
	for _, bad := range claimsOfSolitude {
		if strings.Contains(low, bad) {
			t.Errorf("%s asserts absence: %q contains %q.\n"+
				"  Recall is partial, so Dibs cannot know this. Report what was measured\n"+
				"  (\"no existing agent cleared the threshold\") rather than what it would\n"+
				"  imply if recall were perfect.", where, s, bad)
		}
	}
}

// Dibs may not tell an agent it is alone.
//
// Recall at tier 0 is about 0.3: for two thirds of declarations the right agent is
// not in the top five, so a miss is the COMMON case rather than evidence of
// anything. SKILLS.md tells agents in as many words never to conclude from
// silence that they are alone, and the API then said exactly that, with more
// authority than the document, because a tool result reads as a measurement while
// a document reads as advice.
//
// A reviewer believed it and reported having the field to itself on work another
// agent had declared minutes earlier. That is the most damaging thing this
// product can get wrong: the whole promise is that you find out somebody is
// already doing it, so a false all-clear is worse than no answer at all.
func TestTheOpenedLaneHintDoesNotClaimSolitude(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	res, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "alpha"})
	if err != nil {
		t.Fatalf("setup: register: %v", err)
	}
	tok, _ := res["token"].(string)
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpAckBoard, Token: tok}); err != nil {
		t.Fatalf("setup: ack: %v", err)
	}

	sug := e.openFirstSpace(ctx, tok, "reviewing the matcher end to end",
		overlap.Prediction{ScorerID: "test"}, nil)
	if sug == nil {
		t.Fatal("openFirstSpace returned nothing; this test cannot see the hint it guards")
	}
	mustNotClaimSolitude(t, "the opened-agent hint", sug.Hint)
	low := strings.ToLower(sug.Hint)
	if !strings.Contains(low, "threshold") {
		t.Errorf("the hint does not mention the threshold, so an agent cannot tell a "+
			"miss from an absence: %q", sug.Hint)
	}
	if !strings.Contains(low, "not proof") {
		t.Errorf("the hint does not say a miss proves nothing; avoiding the false claim "+
			"is only half of it, the agent still has to be told: %q", sug.Hint)
	}
}

// The summary line an agent reads alongside the suggestions must not either.
func TestTheMatchSummaryDoesNotClaimSolitude(t *testing.T) {
	opened := agentsHint([]Suggestion{{Agent: "w", Action: "opened"}})
	mustNotClaimSolitude(t, "the opened summary", opened)
	if !strings.Contains(strings.ToLower(opened), "threshold") {
		t.Errorf("the opened summary does not say a measurement happened: %q", opened)
	}
	// The no-suggestions case has always been honest; pin it so it stays that way.
	mustNotClaimSolitude(t, "the empty summary", agentsHint(nil))
}

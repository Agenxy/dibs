package engine

import (
	"slices"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// Nothing takes the screen until the human has pressed something asking it to.
//
// This is the rule the whole notification path is built on, and the one that is
// easy to lose: raising the text box on arrival is fewer steps, reads as more
// responsive, and is a coordination service deciding that its optional question
// outranks whatever the person was doing. A question is by definition something
// its asker can wait for.
//
// Stated as a property over every shape rather than as three examples, because
// the failure would arrive as a NEW shape somebody added without the rule in
// mind.
func TestNoQuestionOpensAnythingUnprompted(t *testing.T) {
	for _, choices := range [][]string{
		nil,
		{"yes"},
		{"a", "b"},
		{"a", "b", "c"},
		{"a", "b", "c", "d"},
	} {
		plan := planAnswer(choices)
		if len(plan.Buttons) == 0 {
			t.Errorf("planAnswer(%d choices) offers no buttons: there is no way to answer "+
				"and no way to decline", len(choices))
			continue
		}
		// A plan that opens something must first offer a way not to.
		if plan.Then != "" && !slices.Contains(plan.Buttons, deferButton) {
			t.Errorf("planAnswer(%d choices) opens a %s but offers no %q: the only way out "+
				"of the notification is to trigger the thing that takes the screen",
				len(choices), plan.Then, deferButton)
		}
		// Three is what a notification carries. A plan that asks for more would
		// be rejected by Ask at delivery time, which is a failure nobody sees.
		if len(plan.Buttons) > 3 {
			t.Errorf("planAnswer(%d choices) wants %d buttons; a notification carries "+
				"three, so this question would fail to reach anybody",
				len(choices), len(plan.Buttons))
		}
	}
}

// Choices the asker stated are the buttons, so answering is one press.
//
// The point of enumerating an answer space is to turn a composition into a
// gesture. A plan that put three stated choices behind a "Choose…" button would
// pass the rule above and defeat the feature.
func TestStatedChoicesBecomeTheButtons(t *testing.T) {
	choices := []string{"rebase", "merge", "leave it"}
	plan := planAnswer(choices)
	if !slices.Equal(plan.Buttons, choices) {
		t.Errorf("planAnswer(%v).Buttons = %v, want the choices themselves: answering a "+
			"question whose answers are known should be one press", choices, plan.Buttons)
	}
	if plan.Then != "" {
		t.Errorf("planAnswer(%v).Then = %q, want \"\": the press IS the answer, so there "+
			"is nothing further to open", choices, plan.Then)
	}
}

// A fourth choice is offered, not dropped.
//
// core.MaxChoices is 4 and a notification carries 3. The gap is the interesting
// case: the tempting implementations are to truncate (the human never sees an
// answer the asker offered) or to refuse the send (a question rejected for
// being well specified).
func TestAFourthChoiceIsStillReachable(t *testing.T) {
	plan := planAnswer([]string{"a", "b", "c", "d"})
	if plan.Then != thenPick {
		t.Errorf("with %d choices Then = %q, want %q: they do not fit as buttons, so the "+
			"only way the human sees all of them is the list", core.MaxChoices, plan.Then, thenPick)
	}
}

// No choices means a text box, on request.
func TestAnOpenQuestionOffersATextBox(t *testing.T) {
	plan := planAnswer(nil)
	if plan.Then != thenPrompt {
		t.Errorf("planAnswer(nil).Then = %q, want %q: a question nobody enumerated can "+
			"only be answered in words", plan.Then, thenPrompt)
	}
}

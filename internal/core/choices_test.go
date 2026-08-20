package core

import (
	"errors"
	"strings"
	"testing"
)

// A choice must be something a person can press and something Dibs can record.
//
// Both cases here end the same way: the send succeeds, the operator presses the
// option they were shown, and the asker goes on waiting with no error anywhere.
// That is the failure this product exists to remove, reached through the
// feature meant to make answering one press.
//
// Found by a pre-release review, the second in the same place: "Later" first,
// then blank labels.
func TestAChoiceMustBePressableAndRecordable(t *testing.T) {
	for _, tc := range []struct {
		name    string
		choices []string
		want    string
	}{
		{
			// The notifier uses a choice as both the visible title and the
			// identifier, so a blank one is an unlabelled button that returns
			// "", and "" is how the engine spells "dismissed".
			name: "blank", choices: []string{"Yes", ""}, want: "empty",
		},
		{name: "whitespace only", choices: []string{"Yes", "   "}, want: "empty"},
		{
			// "Later" is the deferral Dibs adds itself, so an answer with that
			// label is recorded as no answer at all.
			name: "the reserved deferral", choices: []string{"Now", "Later"}, want: "reserved",
		},
		{name: "reserved, other case", choices: []string{"Now", "LATER"}, want: "reserved"},
		{name: "reserved, padded", choices: []string{"Now", " later "}, want: "reserved"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := Admit(&Op{
				Kind: OpSendMessage, To: "peer", MsgType: MsgQuestion,
				Body: "which?", Choices: tc.choices,
			}, DefaultLimits())
			if err == nil {
				t.Fatalf("choices %q were admitted", tc.choices)
			}
			var ce *Error
			if !errors.As(err, &ce) || ce.Code != "E_BAD_ARG" {
				t.Fatalf("refused with %v, want E_BAD_ARG", err)
			}
			if !strings.Contains(ce.Msg, tc.want) && !strings.Contains(ce.Hint, tc.want) {
				t.Errorf("the refusal does not say what is wrong (%q missing): %s / %s",
					tc.want, ce.Msg, ce.Hint)
			}
		})
	}

	// Ordinary choices still pass, or the feature is gone rather than guarded.
	if err := Admit(&Op{
		Kind: OpSendMessage, To: "peer", MsgType: MsgQuestion,
		Body: "which?", Choices: []string{"Ship it", "Hold", "Not now"},
	}, DefaultLimits()); err != nil {
		t.Errorf("ordinary choices were refused: %v", err)
	}
}

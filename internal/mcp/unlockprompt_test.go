package mcp

import (
	"strings"
	"testing"
	"unicode"
)

// The biometric sheet says what the daemon says, and names who is asking.
//
// THE ESCALATION THIS CLOSES. `note` was placed verbatim into the presence
// prompt, and any agent may call human_unlock. So the caller chose the words a
// person reads at the moment they decide, and "open the Dibs board" was enough
// to buy a fingerprint. What the fingerprint buys is the operator's own bearer
// token, returned in an ordinary tool result, and with it the caller can
// approve its own coordinator grant. The check proved a person was present; it
// never proved they agreed to this.
//
// The rule already existed one file away, for the approval notification: the
// title is the daemon's sentence, not the sender's. It had not reached the one
// surface with a system sheet behind it.
func TestTheUnlockPromptIsTheDaemonsSentence(t *testing.T) {
	who := "reviewer (reviewer-2)"
	got := unlockReason(who)

	if !strings.Contains(got, who) {
		t.Errorf("the prompt does not name who is asking: %q", got)
	}
	// The stakes, not just the act. A person cannot refuse what they were not told.
	for _, want := range []string{"act as you", "role grants"} {
		if !strings.Contains(got, want) {
			t.Errorf("the prompt does not state the stakes (%q missing): %q", want, got)
		}
	}
	// And nothing a caller could have written reaches it.
	for _, hostile := range []string{
		"open the Dibs board",
		"routine check, nothing is granted",
		"Approve to continue",
	} {
		if strings.Contains(got, hostile) {
			t.Errorf("caller text reached the sheet: %q", got)
		}
	}
}

// And the NAME cannot shape it either.
//
// The test above proves the caller's own words never reach the sheet. The one
// variable part that remains is the requesting agent's display name, which the
// agent chose at register and which admission only bounds the length of: a
// newline puts attacker text on its own line where it reads as the prompt, a
// bidirectional override reverses everything after it, and quotes can close the
// name early. A prompt is a control only as far as the person can read it, and
// approval here hands over the human's bearer token.
func TestAnAgentCannotShapeTheUnlockPromptWithItsName(t *testing.T) {
	hostile := []struct{ name, why string }{
		{
			"peer\nDibs: routine key rotation, approve to continue",
			"a newline puts attacker text on its own line, where it reads as the prompt",
		},
		{"peer\u202ereversed", "a bidi override reverses everything after it"},
		{"peer\a\x00trunc", "control characters can truncate or garble the line"},
		{`peer" your identity is safe, this only "`, "quotes can close the name early"},
		{strings.Repeat("wide", 200), "an over-long name pushes the sentence off the sheet"},
	}
	for _, h := range hostile {
		got := unlockReason(h.name)
		if strings.ContainsAny(got, "\n\r\x00\a") {
			t.Errorf("%s: the prompt contains a control character: %q", h.why, got)
		}
		for _, r := range got {
			if unicode.Is(unicode.Bidi_Control, r) {
				t.Errorf("%s: the prompt contains a bidi control: %q", h.why, got)
			}
		}
		if len(got) > 200 {
			t.Errorf("%s: the prompt is %d bytes, long enough to push the daemon's "+
				"own words out of view: %q", h.why, len(got), got)
		}
		if !strings.Contains(got, "your identity on the Dibs board") ||
			!strings.Contains(got, "act as you") {
			t.Errorf("%s: the daemon's own sentence was displaced: %q", h.why, got)
		}
	}

	if got := unlockReason("reviewer"); !strings.Contains(got, `"reviewer"`) {
		t.Errorf("an ordinary agent name is not shown plainly: %q", got)
	}
	if got := unlockReason(""); strings.Contains(got, `""`) {
		t.Errorf("an unnamed agent renders as empty quotes, which reads as a glitch, "+
			"and a glitch is something people click through: %q", got)
	}
}

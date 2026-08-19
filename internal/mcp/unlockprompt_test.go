package mcp

import (
	"strings"
	"testing"
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

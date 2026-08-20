package core

import (
	"errors"
	"strings"
	"testing"
)

// One approval performs one effect.
//
// THE HOLE THIS CLOSES. `grant` and `adopt` were admitted independently and
// both executed on approval, while the operator's prompt is a grant-first
// switch that renders only the grant. A request could therefore read
// "make X coordinator?" on the person's screen and, on the single yes that
// answers it, also move a dormant agent's whole mailbox onto the asker.
//
// Neither half was a bug on its own. The display picked one effect to show
// because it assumed one effect existed, and the validator allowed two because
// it checked each field in isolation. Found by a pre-release review rather than
// by either half's own tests, which is what two individually-reasonable
// decisions meeting in the middle looks like.
//
// Asserted against Admit, not Apply, and that is the point: this is an INGRESS
// rule. A rule added to the fold binds every op already in a ledger, and the
// daemon then refuses to replay history it wrote itself.
func TestARequestCannotHideAnAdoptionBehindAGrant(t *testing.T) {
	err := Admit(&Op{
		Kind: OpSendMessage, To: "victim",
		MsgType: MsgRequest, Body: "may I have coordinator?",
		Grant: RoleCoordinator,
		Adopt: "some-dormant-agent",
	}, DefaultLimits())
	if err == nil {
		t.Fatal("a request carrying both a grant and an adoption was admitted. " +
			"Approving it shows the operator only the grant and performs both")
	}

	var ce *Error
	if !errors.As(err, &ce) {
		t.Fatalf("not a coded error: %v", err)
	}
	if ce.Code != "E_BAD_ARG" {
		t.Errorf("refused with %s, want E_BAD_ARG", ce.Code)
	}
	// The hint must name the way through, or an agent with a legitimate reason
	// for both is stopped and told nothing it can act on.
	if !strings.Contains(strings.ToLower(ce.Hint), "two requests") {
		t.Errorf("the hint does not name the way through: %q", ce.Hint)
	}

	// Each alone is still admitted, or the rule has removed the feature rather
	// than the hole.
	for _, tc := range []struct {
		name string
		op   *Op
	}{
		{"grant alone", &Op{
			Kind: OpSendMessage, To: "victim",
			MsgType: MsgRequest, Body: "role please", Grant: RoleCoordinator,
		}},
		{"adopt alone", &Op{
			Kind: OpSendMessage, To: "victim",
			MsgType: MsgRequest, Body: "mailbox please", Adopt: "some-dormant-agent",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := Admit(tc.op, DefaultLimits()); err != nil {
				t.Errorf("%s was refused: %v", tc.name, err)
			}
		})
	}
}

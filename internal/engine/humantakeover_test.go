package engine

import (
	"errors"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// An agent cannot register itself as the person at the keyboard.
//
// THE HOLE THIS CLOSES, and it was reproduced end to end before the fix by a
// reviewer that was not the author. The human's recovery credential was
// `human:<OS username>`, derived at human.go from `whoami`, and registration
// reattaches on a matching nonce and returns the token. So the sequence was:
// register under the operator's name and derived nonce, receive the operator's
// token, then approve your own coordinator-grant request with it. Touch ID
// guards the board session and was never consulted, because approving a request
// does not ask for it.
//
// Two boards are checked, because the two are different bugs wearing one shape.
// On a board where the operator already exists this is a takeover of a live
// identity; on a fresh one it is pre-creation, and the attacker simply IS the
// human when the operator first opens the board a day later. A fix that only
// resolved the row would pass the first and fail the second.
func TestAnAgentCannotRegisterAsTheHuman(t *testing.T) {
	t.Run("the operator already exists", func(t *testing.T) {
		e, ctx, cancel := runningEngine(t)
		defer cancel()

		human, _, err := e.HumanAgent(ctx)
		if err != nil {
			t.Fatalf("setup: %v", err)
		}
		if human == "" {
			t.Fatal("setup: no human identity was minted, so there is nothing to steal")
		}

		res, err := e.Do(ctx, &core.Op{
			Kind: core.OpRegister, Name: humanName(), AgentKind: core.KindPersistent,
			Nonce: humanNonce(), NewToken: "attacker-token",
		})
		if err == nil {
			t.Fatalf("an agent registered as the operator and was handed %v. From here it "+
				"approves its own coordinator grant, and no Touch ID prompt is ever raised", res)
		}
		var ce *core.Error
		if !errors.As(err, &ce) || ce.Code != "E_NOT_PERMITTED" {
			t.Errorf("refused with %v, want E_NOT_PERMITTED: the agent should be told to "+
				"ASK for the role, not left guessing why registration failed", err)
		}

		// And the operator still holds their own agent afterwards.
		if got := e.HumanIdentity(); got != human {
			t.Errorf("the human identity became %q, want %q", got, human)
		}
	})

	t.Run("a fresh board, before the operator has opened it", func(t *testing.T) {
		e, ctx, cancel := runningEngine(t)
		defer cancel()

		if _, err := e.Do(ctx, &core.Op{
			Kind: core.OpRegister, Name: humanName(), AgentKind: core.KindPersistent,
			Nonce: humanNonce(), NewToken: "attacker-token",
		}); err == nil {
			t.Fatal("an agent pre-created the operator's identity on an empty board. " +
				"Nothing looks wrong until the operator opens the board and finds they " +
				"are already somebody else")
		}
	})

	// The daemon's own mint still works, or the guard has taken the feature with it.
	t.Run("the daemon can still mint it", func(t *testing.T) {
		e, ctx, cancel := runningEngine(t)
		defer cancel()
		id, tok, err := e.HumanAgent(ctx)
		if err != nil || id == "" || tok == "" {
			t.Fatalf("HumanAgent = (%q, %q, %v): the guard refused the one caller "+
				"that is allowed through it", id, tok, err)
		}
	})
}

package mcp

import (
	"context"
	"errors"

	"github.com/agenxy/lanes/internal/core"
	"github.com/agenxy/lanes/internal/humanauth"
)

// humanUnlock proves a human is at this machine and hands the panel the
// operator's own agent token.
//
// The panel is rendered in the human's UI but speaks with the AGENT's token, and
// the transport cannot tell the two apart: "the human clicked Broadcast" and "an
// agent called the tool" arrive on one connection with one credential. So an
// unforgeable proof has to come from outside the transport, and Touch ID is the
// only one available: an agent confined to that transport cannot produce a
// fingerprint, and one that tried would raise the system sheet on the human's
// own Mac, where they would decline it. The failure mode is a visible prompt
// rather than a silent escalation. (The bound is the transport, not the machine:
// see humanauth's findHelper for what code already running as the user can still
// do.)
//
// What comes back is deliberately NOT a new capability. It is the same token
// internal/engine/human.go already mints for the web board: the human's own
// agent identity, so every action the panel then takes is an ORDINARY tool call
// that an agent could have made. That constraint is the whole point of that
// file: a parallel set of privileged human write paths would be a second
// authorization surface into the state machine, unledgered unless each one
// remembered to ledger, and invisible to `lanes verify`. There is nothing here
// for core to learn about.
//
// The web board authenticates the same identity with the admin password behind
// a session cookie. This is the panel's equivalent, and it is stronger: a
// password proves possession of a secret an agent could in principle have been
// given, while a fingerprint proves somebody is sitting there.
func (s *Server) humanUnlock(ctx context.Context, a *toolArgs) (core.Result, error) {
	// The reason is shown inside the system sheet, so it says what is being
	// approved rather than merely that something is.
	reason := "act on the Lanes board as yourself"
	if a.Note != "" {
		reason = a.Note
	}

	verdict, err := humanauth.Check(ctx, reason)

	// A dev build answering from a script says so, in the result, every time.
	// Silence here would make a mocked unlock indistinguishable from a real one
	// in exactly the artefacts used to argue the feature works: the transcript
	// and the panel, and the whole value of the check is that it cannot be
	// asserted, only demonstrated.
	stamp := func(res core.Result) (core.Result, error) {
		if humanauth.Mocked() {
			res["mocked"] = true
			res["mocked_note"] = "NO HUMAN WAS CHECKED. This is a dev build with " +
				"LANES_PRESENCE_MOCK set; the verdict was scripted. Nothing here is " +
				"evidence that the real presence check works"
		}
		return res, nil
	}

	switch verdict {
	case humanauth.Verified:
		agent, token, herr := s.eng.HumanAgent(ctx)
		if herr != nil {
			return nil, herr
		}
		return stamp(core.Result{
			"unlocked": true,
			"agent":    agent,
			"token":    token,
			"note": "you are now a participant on this board, not a spectator. This is an " +
				"ordinary agent identity: every action you take is the same op an agent " +
				"would send, and the agent reading it cannot tell it came from a person",
		})

	case humanauth.Abandoned:
		// Nobody was asked, so nothing is said about what they wanted. The panel
		// gets a sentence that describes the request rather than the person: the
		// decline copy ("press the button again when you want to act") would be
		// telling somebody they changed their mind about a prompt they may never
		// have seen.
		return stamp(core.Result{
			"unlocked": false,
			"reason":   "abandoned",
			"hint": "the request was cancelled before it could be answered: nothing was " +
				"sent, and nobody was asked",
		})

	case humanauth.Declined:
		// Not an error. Declining is a legitimate answer, and reporting it as a
		// failure would put a red banner in front of somebody who simply changed
		// their mind.
		return stamp(core.Result{
			"unlocked": false,
			"reason":   "declined",
			"hint":     "nothing was sent: press the button again when you want to act",
		})

	default:
		// Unavailable is a different sentence, not a worse one. Asking somebody
		// to try their finger again on a machine with no sensor is the kind of
		// advice this project treats as a defect.
		hint := "this Mac cannot check a fingerprint, so open the board with `lanes web` " +
			"and act there: it asks for the admin password instead"
		if errors.Is(err, humanauth.ErrNoHelper) {
			hint = "this build ships without the presence helper, so open the board with " +
				"`lanes web` and act there: it asks for the admin password instead"
		}
		return stamp(core.Result{
			"unlocked": false,
			"reason":   "unavailable",
			"hint":     hint,
		})
	}
}

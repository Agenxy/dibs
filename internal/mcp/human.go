package mcp

import (
	"context"
	"errors"
	"strings"
	"unicode"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/humanauth"
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
// remembered to ledger, and invisible to `dibs verify`. There is nothing here
// for core to learn about.
//
// The web board authenticates the same identity with the admin password behind
// a session cookie. This is the panel's equivalent, and it is stronger: a
// password proves possession of a secret an agent could in principle have been
// given, while a fingerprint proves somebody is sitting there.
func (s *Server) humanUnlock(ctx context.Context, a *toolArgs) (core.Result, error) {
	// THE SHEET'S SENTENCE IS THE DAEMON'S, and it names who is asking.
	//
	// It used to be the CALLER's: `note` was placed verbatim into the biometric
	// prompt. Any agent may call this tool, so any agent could choose the words
	// a person reads at the exact moment they are deciding, and a benign
	// sentence like "open the Dibs board" bought the caller a fingerprint. What
	// it bought is not small: on Verified this returns the operator's own
	// bearer token in an ordinary tool result, and with that token the caller
	// can approve its own coordinator grant. The fingerprint proved a person
	// was present, never that they agreed to THIS.
	//
	// The same rule already governs the approval notification a few files away:
	// "The TITLE is the daemon's sentence, not the sender's." It was not
	// applied here, and this is the surface where it matters most, because the
	// system sheet carries an authority no message body has.
	//
	// So the prompt states the stakes and the requester, and `note` stays out
	// of it. It is still recorded below, because what the caller SAYS it wants
	// is worth having in the answer; it is simply not allowed to be the
	// question.
	// AUTHENTICATED FIRST, because the sentence on the sheet is the whole
	// control and it can only say something true about a caller we know.
	//
	// CallerName ANSWERS for an unknown token, with "an unidentified caller",
	// which is right in a log line and wrong here: this raised a system sheet on
	// the operator's screen attributed to that phrase, so anything holding the
	// coordination secret could make the machine ask its human to approve
	// something, and the one field that tells them who is asking said nobody.
	// SECURITY.md claims the requester is resolved "from the authenticated
	// token"; nothing authenticated it. Found by the pre-release review.
	//
	// Physical approval was still required, so this was never a biometric
	// bypass. It is the attribution that was false, and the attribution is what
	// the human decides on.
	if !s.eng.CallerIsKnown(ctx, a.Token) {
		return core.Result{
			"ok": false,
			"why": "human_unlock needs your agent token: the sheet this raises names " +
				"who is asking, and an unidentified caller is exactly what a person " +
				"should not be asked to approve",
			"hint": "register first, then call this with the token register gave you",
		}, nil
	}
	who := s.eng.CallerName(ctx, a.Token)
	reason := unlockReason(who)

	verdict, err := humanauth.Check(ctx, reason)
	if errors.Is(err, humanauth.ErrPromptBusy) {
		// NOBODY WAS ASKED. The busy slot returns Declined so that no caller
		// treating the verdict alone can mistake it for approval, and the
		// handler below reported that as the human saying no, telling the agent
		// to ask them to press the button again. There is no button: there is
		// another sheet already waiting, and raising a second one is the exact
		// thing being prevented. Saying "declined" here also teaches an agent
		// that a decline is worth retrying, which is the opposite of true.
		return core.Result{
			"ok": false,
			"why": "a presence check is already waiting for an answer on this " +
				"machine, so nothing was asked. The sheet on screen belongs to " +
				"whatever asked first, which is not this request",
			"hint": "wait for the prompt already on screen to be answered or " +
				"dismissed, then try again. This is not a refusal by the human, and " +
				"the operator should decline that sheet if they did not start it",
		}, nil
	}

	// A dev build answering from a script says so, in the result, every time.
	// Silence here would make a mocked unlock indistinguishable from a real one
	// in exactly the artefacts used to argue the feature works: the transcript
	// and the panel, and the whole value of the check is that it cannot be
	// asserted, only demonstrated.
	stamp := func(res core.Result) (core.Result, error) {
		if humanauth.Mocked() {
			res["mocked"] = true
			res["mocked_note"] = "NO HUMAN WAS CHECKED. This is a dev build with " +
				"DIBS_PRESENCE_MOCK set; the verdict was scripted. Nothing here is " +
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
			// What the caller said it wanted, recorded rather than shown on the
			// sheet. See the note above the prompt.
			"requested_by":  who,
			"stated_reason": a.Note,
			"unlocked":      true,
			"agent":         agent,
			"token":         token,
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
		hint := "this Mac cannot check a fingerprint, so open the board with `dibs web` " +
			"and act there: it asks for the admin password instead"
		if errors.Is(err, humanauth.ErrNoHelper) {
			hint = "this build ships without the presence helper, so open the board with " +
				"`dibs web` and act there: it asks for the admin password instead"
		}
		return stamp(core.Result{
			"unlocked": false,
			"reason":   "unavailable",
			"hint":     hint,
		})
	}
}

// unlockReason is the sentence shown on the system sheet.
//
// A function so it can be asserted on. The sentence is the daemon's: every word
// of it except the name is fixed text, and the name is resolved by the daemon
// from the token the request authenticated with rather than taken from the
// call.
//
// THE NAME IS STILL THE AGENT'S OWN, and this used to claim otherwise. An agent
// chooses its display name at register, and admission only bounds the length,
// so the one variable part of a biometric prompt was attacker-shaped: newlines
// to push the real sentence out of view, bidirectional overrides to reverse what
// follows, or text imitating the rest of the prompt. A prompt is a security
// control only to the extent the person can read it.
//
// So the name is flattened to one line of printable characters and quoted, and
// it comes first in a sentence whose remainder cannot be moved. What an agent
// can still do is choose a misleading NAME, which is the same thing it can do
// everywhere else on the board and is visible as a name.
func unlockReason(who string) string {
	return "give " + quotedAgentName(who) + " your identity on the Dibs board: it " +
		"will be able to act as you, including approving role grants"
}

// maxPromptName bounds the agent name on a system sheet. Long enough for any
// name worth having, short enough that the daemon's own sentence stays on
// screen next to it.
const maxPromptName = 48

// quotedAgentName renders an agent-chosen name safely inside a prompt.
//
// Control characters, line breaks and bidirectional overrides all go: each is a
// way to make the fixed text around this either invisible or read backwards,
// and a person cannot decline a sentence they cannot see.
func quotedAgentName(who string) string {
	var b strings.Builder
	for _, r := range who {
		switch {
		case r == '"':
			b.WriteRune('\'')
		case unicode.IsControl(r), unicode.Is(unicode.Bidi_Control, r):
			b.WriteRune(' ')
		case !unicode.IsPrint(r):
			b.WriteRune(' ')
		default:
			b.WriteRune(r)
		}
		if b.Len() >= maxPromptName {
			return `"` + strings.TrimSpace(b.String()) + `…"`
		}
	}
	name := strings.TrimSpace(b.String())
	if name == "" {
		// Never an empty pair of quotes: "give "" your identity" reads as a
		// glitch, and a glitch is something people click through.
		return "an agent that did not name itself"
	}
	return `"` + name + `"`
}

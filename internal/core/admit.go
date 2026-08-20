package core

import "strings"

// Ingress validation, kept apart from the fold on purpose.
//
// Admit and Apply were in one file, and the distinction between them is the
// single most expensive thing to get wrong in this package: Apply is also what
// replays the ledger, so a rule added there is retroactive and the daemon
// refuses to boot on data it wrote itself. AGENTS.md lists it first among the
// recurring bug classes. A reader who has to notice a function boundary to see
// which half they are editing is one edit away from that, and the file was at
// its length limit besides.

// Admit rejects an op arriving from a CALLER. Not called during replay.
//
// The distinction is the whole point, and it cost a daemon its own history to
// learn: this check first went into Apply, and Apply is also the fold that
// replays the ledger. A ledger holding ops that were legal when they were
// written then failed to replay under the stricter rule, and the daemon refused
// to start ("replay apply serial 12: E_EMPTY_BODY") on data it had itself
// written and acknowledged.
//
// So Apply must accept everything it has ever accepted, forever. Anything that
// tightens what callers may DO belongs here, at ingress, where it binds new ops
// and leaves history alone. (The size limits inside Apply carry the same latent
// hazard: lower MaxBodyBytes and an existing ledger stops replaying. They
// predate this and are left rather than moved blind, but new rules go here.)
func Admit(op *Op, lim Limits) error {
	// Bounds on replayed metadata, applied at ingress for the reason above: the
	// same strings are already in ledgers on disk, and rejecting them in Apply
	// would stop those daemons booting.
	//
	// Everything here ends up in State and is therefore re-read into memory on
	// every start, forever. The count of dirs and refs was bounded and the
	// SIZE of each was not, which bounds nothing: sixteen refs of two megabytes
	// each is thirty-two megabytes of permanent ledger, accepted silently. The
	// probe that found this pushed a 2 MiB session_id and a slot holding
	// 100,000 holds, and the board took both.
	//
	// A hold is a host resource name ("port:8080"), a ref a file path, an
	// AgentInfo field a harness name: the honest values are tens of bytes, so
	// these ceilings are three orders of magnitude above real use and only ever
	// catch a mistake or an abuse.
	if err := boundStrings(lim.MaxPathBytes, "dirs", op.Dirs); err != nil {
		return err
	}
	if err := boundStrings(lim.MaxPathBytes, "refs", op.Refs); err != nil {
		return err
	}
	if len(op.Holds) > lim.MaxDirs {
		return errTooLarge("holds", lim.MaxDirs)
	}
	if err := boundStrings(lim.MaxPathBytes, "holds", op.Holds); err != nil {
		return err
	}
	if len(op.SessionID) > lim.MaxNameBytes {
		return errTooLarge("session_id", lim.MaxNameBytes)
	}
	// A choice is a button label, so it is bounded as a name and there are few of
	// them. Bounded here rather than in Apply for the reason at the top of this
	// function: these strings are already in ledgers on disk, and a rule added to
	// the fold is retroactive.
	if err := checkGrantRequest(op); err != nil {
		return err
	}
	// Choices only mean something on a QUESTION.
	//
	// Admit bounded them and never asked what they were attached to, and Apply
	// stores them on any message, so send(type:"notify", choices:[...]) reported
	// success and wrote an option set nothing will ever render: a request uses
	// fixed approve and deny, and a notify or a handoff cannot be answered at
	// all. The sender is told it offered a choice and no reader will ever see
	// one. Found by a pre-release review.
	if len(op.Choices) > 0 && op.MsgType != MsgQuestion {
		return errf("E_BAD_TYPE",
			`send it as type "question": a request is approved or denied, and a notify `+
				"or a handoff is not answered at all, so choices on those would be "+
				"recorded and never shown to anybody",
			"a %s cannot carry choices", op.MsgType)
	}
	if len(op.Choices) > MaxChoices {
		return errTooLarge("choices", MaxChoices)
	}
	if err := boundStrings(lim.MaxNameBytes, "choices", op.Choices); err != nil {
		return err
	}
	// "Later" is the daemon's own way out, and cannot also be an answer.
	//
	// Up to three choices become the notification's buttons verbatim, and the
	// engine treats a press equal to its deferral label as "no answer yet". So
	// a question offering ["Now", "Later"] rendered both, and pressing the
	// stated answer recorded nothing: the operator answered, the asker went on
	// waiting, and no layer reported anything wrong. Found by a pre-release
	// review.
	//
	// Refused at ingress rather than renamed at the button, because the sender
	// chose that word and showing them something else is its own quiet lie. The
	// hint says what to write instead. Case-insensitive: the collision is what
	// the person reads, not what the byte comparison happens to match.
	for _, c := range op.Choices {
		// A blank choice becomes a blank BUTTON, and pressing it returns "",
		// which the engine reads as a dismissal. So the send succeeded, the
		// person pressed the option they were offered, and the asker went on
		// waiting with nothing reported wrong anywhere. The notifier uses each
		// value as both the visible title and the identifier, so there is not
		// even anything to render. Found by a pre-release review.
		if strings.TrimSpace(c) == "" {
			return errf("E_BAD_ARG",
				"give every choice a label: a blank one renders as an empty button, "+
					"and pressing it is indistinguishable from dismissing the question",
				"a choice is empty")
		}
		if strings.EqualFold(strings.TrimSpace(c), "later") {
			return errf("E_BAD_ARG",
				`pick another word: "Defer", "Not now", "Ask again" all work. `+
					`"Later" is the button Dibs adds itself for "no answer yet", so an `+
					`answer with that label would be recorded as no answer at all`,
				`"Later" is reserved: it is the deferral Dibs offers on every question`)
		}
	}
	if a := op.Agent; a != nil {
		for field, v := range map[string]string{
			"agent.harness": a.Harness, "agent.version": a.Version,
			"agent.surface": a.Surface, "agent.model": a.Model,
			"agent.provider": a.Provider, "agent.effort": a.Effort,
			"agent.title": a.Title, "agent.project": a.Project,
			"agent.branch": a.Branch, "agent.host": a.Host,
		} {
			if len(v) > lim.MaxNameBytes {
				return errTooLarge(field, lim.MaxNameBytes)
			}
		}
		// A cwd is a PATH, and was bounded as if it were a name. 128 bytes is
		// generous for a model or a branch and ordinary for a working
		// directory: a macOS temp directory alone reaches ninety, and any
		// checkout a few levels inside a home directory passes it. The whole
		// register was then refused, so the agent could not coordinate AT
		// ALL, over a descriptive field. Relaxing an admission bound is safe in
		// the direction that matters: Admit runs only on ingress, so nothing
		// already in a ledger becomes inadmissible.
		for field, v := range map[string]string{
			"agent.cwd": a.CWD, "agent.repo_dir": a.RepoDir,
			"agent.repo_remote": a.RepoRemote, "agent.repo_roots": a.RepoRoots,
		} {
			if len(v) > lim.MaxPathBytes {
				return errTooLarge(field, lim.MaxPathBytes)
			}
		}
	}
	switch op.Kind {
	case OpSpaceAnnounce:
		// An announcement with nothing in it obliges every member to
		// acknowledge nothing, and re-pings them until they do. The UPPER bound
		// on a body was checked and the lower one was not.
		//
		// Not hypothetical: a whole coordination space between two agents ran
		// on empty announcements, because the caller sent the text under the
		// wrong key and the missing value became "". Each returned a serial and
		// a must_ack count, so it looked delivered from the sending side, while
		// the receiving agent saw an agent full of obligations that said nothing
		// and had to ask a human what was going on.
		if strings.TrimSpace(op.Body) == "" {
			return errf("E_EMPTY_BODY",
				"pass the announcement text as `body`",
				"`body` is empty: an announcement needs something to say, because it "+
					"obliges every member to acknowledge it, and an empty one obliges "+
					"them to acknowledge nothing")
		}
	case OpSpacePost:
		// A post obliges nobody, so an empty one is noise rather than a false
		// obligation, but it is still an event delivered to every member, and
		// the cause is the same slip.
		if strings.TrimSpace(op.Body) == "" {
			return errf("E_EMPTY_BODY", "pass the text as `body`",
				"`body` is empty: a post needs something to say")
		}
	case OpSendMessage:
		// `adopt` is copied permanently into the message and therefore into the
		// ledger, before anything looks up whether that agent exists. It was
		// checked for message TYPE and never for length, so one authenticated
		// request could put most of the 96 MiB request cap of nonsense into the
		// encrypted ledger and into every replay of it, forever. An agent id is
		// a name; it is bounded like one. Found by a pre-release review.
		if len(op.Adopt) > lim.MaxNameBytes {
			return errTooLarge("adopt", lim.MaxNameBytes)
		}
	case OpUpdate:
		// Both of these were added to Apply, which is the mistake this file
		// exists to stop.
		//
		// Apply is also the fold. A bound enforced there is retroactive: lower
		// the limit in a later build and an op this release accepted and
		// ledgered makes that daemon refuse its own history at boot. The bounds
		// that predate this are left where they are rather than moved blind, and
		// the rule written at the top of this file is that NEW ones come here.
		// These two are new, and they went to the wrong place; a pre-release
		// review found them.
		//
		// Removing them from the fold is safe in the one direction that matters:
		// a fold can only break replay by getting STRICTER, never by getting
		// more permissive. Every op already on disk was admitted under these
		// same numbers.
		if len(op.Name) > lim.MaxNameBytes {
			return errTooLarge("name", lim.MaxNameBytes)
		}
		if len(op.Description) > lim.MaxDescBytes {
			return errTooLarge("description", lim.MaxDescBytes)
		}
	case OpSpaceRetitle:
		if len(op.Text) > lim.MaxNameBytes {
			return errTooLarge("topic", lim.MaxNameBytes)
		}
	}
	return nil
}

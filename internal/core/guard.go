package core

import "time"

// Guarding: turning a claim from a note into something that actually holds.
//
// Until now a claim was purely advisory — it told a well-behaved agent to stay
// away and did nothing whatsoever to one that never looked. That is the honest
// floor for a coordination service, and it is also the gap that makes fleets
// corrupt each other's work: the agent that damages your edit is precisely the
// one that never read the board.
//
// Every harness Lanes supports can ask a question before it edits a file, and
// refuse the edit on the answer. So the claim can be enforced at the moment it
// matters, by the harness itself, without Lanes ever driving anything.
//
// THE DESIGN CONSTRAINT, from claims.go and worth restating because it is the
// thing most likely to be got wrong later:
//
//	two agents editing the same file is NORMAL — version control solved that,
//	and suppressing it would destroy fleet parallelism
//
// So this must NOT block on incidental overlap. It blocks on one signal only:
// somebody took an EXCLUSIVE claim, which is the explicit act of saying "leave
// me alone here". Everything else is allowed through, silently.

// GuardVerdict is the answer to "may this lane write this path right now?"
type GuardVerdict struct {
	// Decision is allow | deny | ask, matching the vocabulary every harness
	// hook already speaks.
	Decision string `json:"decision"`
	// Reason is written for the agent that is about to be stopped, so it names
	// the holder and what to do next rather than merely refusing.
	Reason string `json:"reason,omitempty"`
	// Holder is the lane whose claim caused a non-allow verdict.
	Holder string `json:"holder,omitempty"`
	// Path is the claim that matched, which may be an ancestor of the file.
	Path string `json:"path,omitempty"`
}

// Guard verdicts, using the vocabulary every harness hook already speaks.
const (
	GuardAllow = "allow"
	GuardDeny  = "deny"
	GuardAsk   = "ask"
)

var guardAllowed = GuardVerdict{Decision: GuardAllow}

// GuardPath decides whether lane may write path.
//
// `lane` is the id of the lane asking, or "" when the caller could not be
// resolved to one. An unresolved caller is ALLOWED: most sessions are not
// registered lanes, and a coordination tool that blocks every editor it does
// not recognise is a broken editor, not a safe one.
func (s *State) GuardPath(lane, path string, now time.Time) GuardVerdict {
	// An unresolved caller is ALLOWED, and this early return is the whole of
	// that promise — without it the loop below treats every claim as somebody
	// else's and denies, because "" never equals a holder's id.
	//
	// Getting this wrong blocks every editor on the machine that is not a
	// registered lane, including the human's own. Found by running the tool
	// rather than by reading this function, which is the argument for running
	// the tool.
	if lane == "" || path == "" {
		return guardAllowed
	}
	p := cleanPath(path)

	// The strongest matching claim wins, and a live holder outranks a stale one
	// — being told "no" by someone who is still working is more actionable than
	// being told "maybe" by someone who vanished.
	verdict := guardAllowed
	for _, c := range s.Claims {
		if c.Lane == lane || c.Mode != ClaimExclusive || !pathsOverlap(c.Path, p) {
			continue
		}
		// A subagent is its parent's work, not a third party to it.
		//
		// This is the ordinary delegation pattern — claim the area, spawn a
		// subagent to edit it — and without this the guard DENIES that subagent
		// on its own parent's claim, telling it to "coordinate with lane parent"
		// and "pick different work". Because the guard is an enforcement path
		// rather than advice, the harness then refuses the edit outright: the
		// exclusive claim locks out the very work it was taken for. Observed
		// exactly, for children and grandchildren alike.
		//
		// Only this direction. A parent editing inside its SUBAGENT's claim is
		// still stopped: the child asked not to be disturbed there, and the
		// parent can force_release if it means to overrule that.
		if s.DescendsFrom(lane, c.Lane) {
			continue
		}
		holder := s.Lanes[c.Lane]
		// A claim whose lane is gone entirely is archaeology, not coordination.
		if holder.Gone() {
			continue
		}

		if holder.Status == StatusActive {
			return GuardVerdict{
				Decision: GuardDeny,
				Holder:   c.Lane,
				Path:     c.Path,
				Reason: "lane " + c.Lane + " holds an exclusive claim on " + c.Path +
					" and is active. Coordinate with it before editing here — send it a " +
					"request, or pick different work. If it is finished, ask it to release " +
					"the claim.",
			}
		}

		// The holder stopped checking in. SPEC §claims is explicit that an
		// expired claim is "loss of coordination, not proof it is safe to
		// proceed" — so this is neither a clean allow nor a fair deny. Handing
		// it to the human as a prompt is the honest third answer, and the only
		// one that cannot silently lose someone's work OR wedge the fleet
		// behind a crashed agent.
		if verdict.Decision == GuardAllow {
			verdict = GuardVerdict{
				Decision: GuardAsk,
				Holder:   c.Lane,
				Path:     c.Path,
				Reason: "lane " + c.Lane + " holds an exclusive claim on " + c.Path +
					" but has gone quiet (" + string(holder.Status) + "). Its claim may be " +
					"stale, or it may be mid-edit and simply slow. Proceeding is your call.",
			}
		}
	}
	return verdict
}

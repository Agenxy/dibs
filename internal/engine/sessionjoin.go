package engine

import (
	"path/filepath"
	"strings"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// Joining the names one harness session goes by, because its two halves do not
// agree on one and each can only say its own.
//
// THE FAILURE THIS FIXES. A Codex agent was never woken by anything. Mail sat
// unread while every call in the chain returned success, on the exact
// configuration shipped in ~/.codex/hooks.json.
//
// The cause is an identifier that is right on both sides and matches on
// neither. The stdio bridge cannot see the session, so it derives `host-<ppid>`
// from the process that spawned it: correct, and observable by an IN-PROCESS
// plugin, which is why opencode works. Codex's hooks are configured rather than
// in-process, and send what Codex calls the session, a uuid. So the agent
// registers as `host-10602`, its own Stop hook asks about
// `codex-...`, AgentBySession matches nothing, and the digest comes back empty.
// Neither half is wrong and no configuration can reconcile them, because
// neither can pronounce the other's name.
//
// It is worth being precise about this, because the obvious reading, that the
// bridge simply has no session id, is wrong and produces a fix that changes
// nothing: it has one, and it is a different one. That version was written, and
// it left this exact probe still failing.
//
// The join was already possible. SessionStart calls hook_session with
// (session_id, cwd) BEFORE the model has done anything, so the daemon knows the
// harness's own name for the session and where it is running. Registration
// arrives moments later from the same directory under the bridge's name.
// Nothing put the two together, so the agent answered to one of them.
//
// WHY THE JOIN IS HERE AND NOT IN hook_poll. The obvious fix is to let
// hook_poll fall back to matching on cwd, and the schema even documents that
// ("used to find the agent when the harness's session id differs"). It is the
// wrong fix, and this repository has the scar: hook_poll carries no token by
// design, so a cwd fallback there handed an unregistered session another
// agent's private mail INCLUDING the body, and told a stranger that a path
// under exclusive claim was unclaimed. core.AgentForHook now refuses it, and
// that refusal stays.
//
// Registration and check_in are the opposite kind of call. They are on the
// authenticated bridge connection, they are the agent saying where it is, and
// the name it adopts becomes its own rather than a key for reading somebody
// else's. So the join happens there, and hook_poll keeps looking up exactly one
// thing: a session name that an agent holds.
//
// WHAT STOPS THIS BEING THE OLD BUG IN A NEW PLACE. Ambiguity refuses. A
// planted announcement does not steal the session, because the harness that is
// really there announces too, and two candidates for one directory means the
// daemon declines to guess. The attack degrades the feature instead of
// disclosing anything, which is the correct direction for a wrong guess here.
// Claimed sessions are never re-adopted, and a stale announcement expires.

// sessionJoinWindow is how long an announced session stays adoptable.
//
// SessionStart to the model's first register call is seconds when the harness
// registers on its own, and minutes when a person reads something first. An
// hour is far past both and still short enough that yesterday's dead session
// cannot be adopted by today's.
const sessionJoinWindow = time.Hour

// maxSessionIDBytes bounds what the join will adopt.
//
// The bound is HERE rather than in Apply because this is the only thing that
// puts a session id on an update op, and a length rule inside the fold would
// bind every op already in the ledger. Harness session ids are uuids; anything
// past this is not one, and the announcement is ignored rather than stored.
const maxSessionIDBytes = 256

// announcedSession returns the session id a harness reported at `cwd` through
// its own lifecycle hook and that no agent has claimed, or "".
//
// The decision, split from exec() so it can be tested without a loop: on a
// zero-value Engine, e.query() sends on a nil channel and blocks forever
// instead of failing, so anything only reachable through the wrapper is
// effectively untested. See AGENTS.md.
func announcedSession(children map[string]Child, st *core.State, cwd string, now time.Time) string {
	if cwd == "" || len(children) == 0 {
		return ""
	}
	want := cleanDir(cwd)
	if want == "" {
		return ""
	}
	var found string
	for sid, c := range children {
		if sid == "" || len(sid) > maxSessionIDBytes || cleanDir(c.CWD) != want {
			continue
		}
		if now.Sub(c.Seen) > sessionJoinWindow {
			continue // announced too long ago to be the session registering now
		}
		// Already somebody's. Adopting it would move another agent's mail
		// delivery onto this one, which is the disclosure this whole path is
		// careful about.
		if st != nil && st.AgentBySession(sid) != nil {
			continue
		}
		if found != "" {
			// Two sessions in one directory is the normal state of a repo with
			// two agents in it, and it is also what a planted announcement
			// looks like. Both are answered the same way: guess nothing.
			return ""
		}
		found = sid
	}
	return found
}

// cleanDir normalises a directory for comparison, matching how core compares
// the paths agents register with.
func cleanDir(p string) string {
	if p == "" {
		return ""
	}
	p = filepath.Clean(p)
	if len(p) > 1 {
		p = strings.TrimSuffix(p, "/")
	}
	return p
}

// mayClaimSession reports whether a caller may bind a session id it named
// itself.
//
// A harness volunteering "I am thread X" is an identity CLAIM arriving on an
// authenticated connection, not proof. The rule is the one the announced
// session already uses, for the same reason: a session another agent holds is
// never taken, because binding it would move that agent's wake delivery onto
// this one. Whoever holds it keeps it.
//
// Deliberately no cleverness beyond that. The claim is only reachable on an
// authenticated call, the id it names is the harness's own, and the thing it
// buys is that hooks and rings find this agent instead of nobody. An attacker
// who can make authenticated calls as an agent can already read that agent's
// mail directly.
func (e *Engine) mayClaimSession(sid, token string) bool {
	if sid == "" {
		return false
	}
	if len(sid) > maxSessionIDBytes {
		return false
	}
	holder := e.state.AgentBySession(sid)
	if holder == nil {
		return true // unclaimed
	}
	// Already ours is fine and idempotent; already somebody else's is not.
	if e.state.AgentByToken(token) == holder {
		return true
	}
	// UNLESS THE HOLDER ONLY GUESSED IT.
	//
	// A binding the daemon inferred by directory is an assumption, not a claim,
	// and it is wrong in exactly one situation: two sessions share a directory
	// and the agent holding the id was swept while its session kept running. The
	// agent that actually IS this session states the id on every call it makes;
	// the one that inherited it never said anything about it.
	//
	// So a stated claim takes an inferred binding back, and takes nothing from
	// an agent that stated its own. Without this the mis-binding is permanent:
	// the rightful session is refused its own id and the holder has no reason to
	// notice it is holding one. That was the state of this project's own board
	// for hours, with one agent's mail announced into another's context.
	return holder.SessionGuessed
}

package engine

import (
	"context"
	"time"

	"github.com/agenxy/lanes/internal/core"
)

// GuardPath answers a harness's pre-edit hook: "may this session write here?"
//
// This is what turns a claim from a note into something that holds. Until now a
// claim only informed an agent that bothered to look, and the agent that
// damages your work is exactly the one that never looked. Every harness Lanes
// supports can ask before it edits and refuse on the answer, so the claim is
// enforced at the moment it matters, by the harness, with Lanes still never
// driving anything. Same category as the hook_poll wake path.
//
// It takes a session id for the same reason HookPoll does: a hook knows
// "${session_id}" from its own input and has nowhere safe to keep a token.
//
// It FAILS OPEN, deliberately and in three ways: an unknown session, an
// unregistered lane, and any internal trouble all return allow. A coordination
// service that bricks the editor when it is confused is not a safe tool, it is
// a broken one, and the blast radius of a missed guard is a merge conflict,
// while the blast radius of a false deny is an engineer who turns Lanes off.
func (e *Engine) GuardPath(ctx context.Context, sessionID, path, cwd string) (core.Result, error) {
	return e.query(ctx, func() core.Result {
		lane := ""
		if l := e.state.LaneForHook(sessionID, cwd); l != nil {
			lane = l.ID
		}
		// Counted whether or not it resolved: a guard that never resolves is
		// inert, and only the daemon can see that (hookhealth.go).
		e.noteHook("guard", lane != "")
		v := e.state.GuardPath(lane, path, time.Now())
		out := core.Result{"decision": v.Decision}
		if v.Decision == core.GuardAllow {
			// WHY it was allowed, because the two reasons are not the same fact
			// and one of them means the guard is not protecting anything.
			//
			// "nothing claims this path" is the guard working. "I could not tell
			// which agent you are" is the guard FAILING OPEN: deliberately, and
			// correctly, since blocking every editor it cannot identify would be
			// a broken editor rather than a safe one. But the two were
			// indistinguishable in the reply, so a misconfigured session id made
			// the guard silently inert and looked exactly like a clean board.
			// That happened, and cost a day: opencode's plugin sent its own
			// session id while the bridge had registered the lane under another.
			if lane == "" {
				out["basis"] = "unidentified-session"
				out["hint"] = "no agent could be resolved for this session, so no claim " +
					"could apply and the edit was allowed. This is fail-open by design, " +
					"NOT a finding that the path is unclaimed. If this session belongs to " +
					"a registered agent, its session id does not match: check `lanes doctor`"
			} else {
				out["basis"] = "no-claim"
				out["agent"] = lane
			}
			// Say nothing further on the happy path, and in particular do NOT
			// emit permissionDecision:"allow".
			//
			// In the PreToolUse contract "allow" does not mean "no objection",
			// it means SKIP THE PERMISSION PROMPT. Returning it here would make
			// Lanes silently auto-approve every edit to an unclaimed path, which
			// is a far larger change to the user's safety posture than anything
			// this feature is for. Omitting the field leaves the normal flow
			// exactly as it was.
			return out
		}
		out["holder"] = v.Holder
		out["path"] = v.Path
		out["reason"] = v.Reason
		// The shape Claude Code parses out of an mcp_tool hook. Other harnesses
		// read the flat fields above; both travel in one result so a single tool
		// serves every pre-edit hook without per-harness variants.
		out["hookSpecificOutput"] = map[string]any{
			"hookEventName":            "PreToolUse",
			"permissionDecision":       v.Decision, // deny | ask
			"permissionDecisionReason": "Lanes: " + v.Reason,
		}
		return out
	})
}

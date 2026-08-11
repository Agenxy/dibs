package engine

import (
	"context"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// Child is what a spawned agent told us about itself.
//
// Reported by the child's own lifecycle hooks, not inferred. That matters most
// for TranscriptPath: the process-forensics layer discovers it by asking a pid
// which files it has open, which works and is a guess; a child that announces
// its own transcript removes the guess.
type Child struct {
	SessionID  string
	Parent     string // the agent that owns it, "" until attributed
	CWD        string
	Model      string
	Transcript string
	AgentID    string // set for a nested subagent
	AgentType  string
	// Progress is a monotonic counter the child reports for itself, in whatever
	// unit it likes. It exists for harnesses whose stores Dibs cannot read,
	// opencode keeps sessions in SQLite and shares one log across every run, so
	// there is no per-process signal to parse. A child that can count its own
	// turns needs none.
	Progress int64
	State    string // "running" | "blocked" | "finished"
	Blocked  string // what it is waiting for permission to do
	// Turn is the turn the child blocked in, when its harness reports one. A
	// blocked child is waiting for a PERSON, so the useful thing to hand back is
	// not "it is blocked" but where to go and answer it.
	Turn  string
	Since time.Time // when it entered State
	Seen  time.Time // last time it said anything at all
}

// NoteChildSession records a lifecycle event from a spawned agent.
//
// Ephemeral rather than ledgered, deliberately. The ledger is the record of
// what agents AGREED, claims, mail, membership, and replaying it must
// reproduce the board exactly. Which processes happen to be running is an
// observation about this machine at this moment: it does not survive a restart
// and should not, because an agent's children are all gone by then anyway.
// Putting it in the ledger would also mean every hook firing on every turn
// wrote a durable op, which is a lot of fsync for a fact with a lifetime of
// minutes.
//
// Never an error when nothing matches. Most sessions have no agent, and a hook
// that fails loudly on every turn of every unrelated session is a hook a person
// removes.
func (e *Engine) NoteChildSession(ctx context.Context, c Child) (core.Result, error) {
	return e.query(ctx, func() core.Result { return e.noteChild(c, time.Now()) })
}

// noteChild is the decision, separated from the loop plumbing.
//
// Split out because the wrapper cannot be tested: query() sends on e.ops, which
// is nil on a zero-value Engine, so a test that built one and called the
// exported method blocked forever rather than failing: it hung CI for five
// minutes before a timeout killed it. The logic worth testing is here, and it
// takes its clock as an argument for the same reason.
func (e *Engine) noteChild(c Child, now time.Time) core.Result {
	if c.SessionID == "" {
		return core.Result{"ok": false, "why": "no session_id: nothing to record this against"}
	}
	if e.children == nil {
		e.children = map[string]Child{}
	}
	prev, known := e.children[c.SessionID]
	if known {
		// A later event carries less than SessionStart did. Stop has no
		// transcript_path, so fields are merged rather than replaced.
		// Overwriting would lose the transcript exactly when supervision needs
		// it most, at the end.
		c = mergeChild(prev, c)
	}
	if c.State == "" {
		// An unrecognised event must not reset what is known. A harness adding
		// an event should leave a blocked child blocked, not quietly mark it
		// running again.
		c.State = prev.State
		if c.State == "" {
			c.State = "running"
		}
	}
	if !known || prev.State != c.State {
		c.Since = now
	}
	c.Seen = now
	// Attribution: the child's own cwd is the strongest link Dibs already has,
	// because agents register with theirs. A fallback for the environment ladder
	// in internal/liveness, not a replacement: two agents in one repo share a
	// cwd, and the environment does not.
	if c.Parent == "" && e.state != nil {
		if l := e.state.LaneForHook(c.SessionID, c.CWD); l != nil {
			c.Parent = l.ID
		}
	}
	e.children[c.SessionID] = c
	return core.Result{
		"ok": true, "session_id": c.SessionID, "state": c.State,
		"parent": c.Parent, "watched": c.Transcript != "",
		// Echoed so a caller can confirm its counter landed and did not go
		// backwards. A monotonic value a reporter cannot read back is a
		// contract with no way to check it.
		"progress": c.Progress,
	}
}

// mergeChild keeps what a later, thinner event does not carry.
func mergeChild(prev, next Child) Child {
	keep := func(a, b string) string {
		if b != "" {
			return b
		}
		return a
	}
	next.CWD = keep(prev.CWD, next.CWD)
	next.Model = keep(prev.Model, next.Model)
	next.Transcript = keep(prev.Transcript, next.Transcript)
	next.AgentID = keep(prev.AgentID, next.AgentID)
	next.AgentType = keep(prev.AgentType, next.AgentType)
	next.Parent = keep(prev.Parent, next.Parent)
	next.Turn = keep(prev.Turn, next.Turn)
	// Monotonic by contract, so a later event that omits it must not reset it,
	// and one that goes BACKWARDS is a restarted counter, not lost work.
	if next.Progress < prev.Progress {
		next.Progress = prev.Progress
	}
	// Since is carried too, and its absence was not a small thing.
	//
	// noteChild resets Since only when the STATE changes, which is right: the
	// field means "how long has it been in this state". But no caller sets Since
	// on an incoming event, so an event that left the state alone merged a zero
	// time, and the sweep reported since_seconds of 9223372036, about 292 years,
	// on a child that had been running for a moment. Two ordinary lifecycle
	// events in a row were enough.
	//
	// It belongs here rather than in noteChild's else-branch because this
	// function's entire job is keeping what a later, thinner event does not
	// carry, and Since is exactly that.
	if next.Since.IsZero() {
		next.Since = prev.Since
	}
	return next
}

// Children returns the spawned agents currently known, for the supervision
// sweep and for an operator asking what is running.
func (e *Engine) Children(ctx context.Context) (core.Result, error) {
	return e.query(ctx, func() core.Result {
		out := make([]map[string]any, 0, len(e.children))
		for _, c := range e.children {
			out = append(out, map[string]any{
				"session_id": c.SessionID, "parent": c.Parent, "state": c.State,
				"blocked_on": c.Blocked, "blocked_turn": c.Turn,
				"transcript": c.Transcript, "progress": c.Progress,
				"cwd": c.CWD, "model": c.Model, "agent_id": c.AgentID,
				"since_seconds": time.Since(c.Since).Seconds(),
				"seen_seconds":  time.Since(c.Seen).Seconds(),
			})
		}
		return core.Result{"children": out}
	})
}

// StateForEvent maps a harness lifecycle event onto what it means for
// supervision. Unrecognised events keep the child in whatever state it was in,
// because a harness adding an event should not silently reset anything.
func StateForEvent(event string) string {
	switch event {
	case "SessionStart", "SubagentStart", "UserPromptSubmit":
		return "running"
	case "PermissionRequest":
		return "blocked"
	case "Stop", "SessionEnd", "SubagentStop":
		return "finished"
	}
	return ""
}

// HookTrafficSeen reports whether a harness lifecycle hook has fired for this
// session: the only evidence the daemon has that a plugin is LIVE.
//
// Installed on disk and actually loaded are different claims, and only the
// second one matters: hooks are read at session start, so a plugin installed
// mid-session is inert until the next one, and from the agent's side those two
// states are indistinguishable. This is distinguishable, because a SessionStart
// hook that fired left a record here.
//
// The ordering is what makes it usable at registration time. SessionStart runs
// before the agent gets a turn, so by the time it calls register the answer
// is already known. Dibs can tell it whether its own wake path works without
// asking it to test anything.
//
// A false answer means "no hook traffic seen for this session", never "the
// plugin is not installed". Those differ, and the daemon can only see the first.
func (e *Engine) HookTrafficSeen(ctx context.Context, sessionID string) bool {
	if sessionID == "" {
		return false
	}
	res, err := e.query(ctx, func() core.Result {
		_, ok := e.children[sessionID]
		return core.Result{"seen": ok}
	})
	if err != nil {
		return false
	}
	seen, _ := res["seen"].(bool)
	return seen
}

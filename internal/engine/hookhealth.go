package engine

import (
	"sync/atomic"
	"time"
)

// Is anything actually asking?
//
// The claim guard fails open when it cannot resolve the caller to an agent, and
// that is the right behaviour: blocking every editor it cannot identify would
// be a broken editor rather than a safe one. But it means a guard that is
// wired wrong is INDISTINGUISHABLE from a board where nothing is claimed: every
// call returns allow, every test passes, and the fleet is unprotected.
//
// That is not a hypothetical. A mismatched session id: opencode's plugin
// sending its own id while the bridge had registered the agent under another,
// left the guard inert for a day. Nothing anywhere said so.
//
// The daemon is the one party that can see it, because it sees every call and
// whether it resolved. Two counters make the invisible failure diagnosable:
//
//   - resolved == 0 and unresolved > 0 → hooks ARE wired, and not one of them
//     names an agent this board knows. This is the day-costing bug, exactly.
//   - both zero → nothing is asking at all: the hook or plugin is not installed.
//
// Ephemeral and lock-free: this is operational telemetry, not coordination
// state, and it must never cost a mutex on the request path.
type hookHealth struct {
	guardResolved   atomic.Int64
	guardUnresolved atomic.Int64
	pollResolved    atomic.Int64
	pollUnresolved  atomic.Int64
	lastAt          atomic.Int64 // unix nanos of the most recent call of any kind
}

// HookHealth is what `dibs doctor` reads.
type HookHealth struct {
	GuardResolved   int64     `json:"guard_resolved"`
	GuardUnresolved int64     `json:"guard_unresolved"`
	PollResolved    int64     `json:"poll_resolved"`
	PollUnresolved  int64     `json:"poll_unresolved"`
	Last            time.Time `json:"last,omitempty"`
	// Verdict and Hint name the situation and the fix, so a diagnostic does not
	// leave the reader to infer either.
	Verdict string `json:"verdict"`
	Hint    string `json:"hint,omitempty"`
}

func (e *Engine) noteHook(kind string, resolved bool) {
	e.hooks.lastAt.Store(time.Now().UnixNano())
	switch {
	case kind == "guard" && resolved:
		e.hooks.guardResolved.Add(1)
	case kind == "guard":
		e.hooks.guardUnresolved.Add(1)
	case resolved:
		e.hooks.pollResolved.Add(1)
	default:
		e.hooks.pollUnresolved.Add(1)
	}
}

// HookHealth reports whether the harness integrations are actually working.
func (e *Engine) HookHealth() HookHealth {
	h := HookHealth{
		GuardResolved:   e.hooks.guardResolved.Load(),
		GuardUnresolved: e.hooks.guardUnresolved.Load(),
		PollResolved:    e.hooks.pollResolved.Load(),
		PollUnresolved:  e.hooks.pollUnresolved.Load(),
	}
	if ns := e.hooks.lastAt.Load(); ns > 0 {
		h.Last = time.Unix(0, ns)
	}
	total := h.GuardResolved + h.GuardUnresolved + h.PollResolved + h.PollUnresolved
	resolved := h.GuardResolved + h.PollResolved

	switch {
	case total == 0:
		h.Verdict = "never-called"
		h.Hint = "no harness has ever asked this daemon a lifecycle question, so the " +
			"claim guard has never run and mail is never injected. Install the plugin or " +
			"hook for your harness (see plugins/), then start a NEW agent session. " +
			"running sessions do not reload their config"
	case resolved == 0:
		// The exact signature of the bug that cost a day.
		h.Verdict = "never-resolved"
		h.Hint = "harness hooks ARE reaching this daemon, but not one call has resolved " +
			"to a registered agent, so the guard has allowed every edit and injected no " +
			"mail. The session id the hook sends does not match the one the agent " +
			"registered with. This looks exactly like a board where nothing is claimed"
	case h.GuardResolved == 0 && h.GuardUnresolved > 0:
		h.Verdict = "guard-unresolved"
		h.Hint = "the wake path works but no guard call has resolved to an agent, so " +
			"claims are advisory only. The pre-edit hook is sending a different session " +
			"id from the one the agent registered with"
	case h.GuardUnresolved > h.GuardResolved:
		h.Verdict = "guard-mostly-unresolved"
		h.Hint = "most guard calls do not resolve to an agent, so most edits are " +
			"unprotected. Some harness is sending a session id this board does not know"
	default:
		h.Verdict = "ok"
	}
	return h
}

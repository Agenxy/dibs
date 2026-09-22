package engine

import (
	"sync/atomic"
	"time"

	"github.com/agenxy/dibs/internal/core"
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
//
// "Unresolved" is two facts, and only one of them is a fault. A session that
// was never registered resolves to nobody, correctly: the plugin is installed
// machine-wide, so every session of that harness asks, including the ones
// whose agent never called register. That is usage, and it is counted as a
// STRANGER: no agent is active in the directory the hook names. The fault is
// the other case: an agent IS active there and the hook still names nobody,
// which means the hook and the registration carry different session ids and
// that agent is unwakeable. `dibs doctor` read the sum for a month and called
// a board of unregistered sessions a broken guard.
type hookHealth struct {
	guardResolved   atomic.Int64
	guardUnresolved atomic.Int64 // a live agent works there and was not matched
	guardStrangers  atomic.Int64 // nobody is active there: an unregistered session
	pollResolved    atomic.Int64
	pollUnresolved  atomic.Int64
	pollStrangers   atomic.Int64
	lastAt          atomic.Int64 // unix nanos of the most recent call of any kind
}

// HookHealth is what `dibs doctor` reads.
type HookHealth struct {
	GuardResolved   int64 `json:"guard_resolved"`
	GuardUnresolved int64 `json:"guard_unresolved"`
	// Strangers are unresolved calls from directories where no agent is
	// active: sessions that never registered, not a misbinding. They are
	// reported separately and never make the verdict a fault.
	GuardStrangers int64     `json:"guard_strangers"`
	PollResolved   int64     `json:"poll_resolved"`
	PollUnresolved int64     `json:"poll_unresolved"`
	PollStrangers  int64     `json:"poll_strangers"`
	Last           time.Time `json:"last,omitempty"`
	// Verdict and Hint name the situation and the fix, so a diagnostic does not
	// leave the reader to infer either.
	Verdict string `json:"verdict"`
	Hint    string `json:"hint,omitempty"`
}

// hookStranger decides, for a lifecycle call that resolved to nobody, whether
// the caller is an unregistered session (a stranger) or a registered agent the
// hook cannot reach (a misbinding).
//
// The first discriminator was "is any agent active in the directory the hook
// named". That reads a live agent as the possible caller, which is only right
// while its own hooks have never resolved. Once they have, the agent is
// reachable, and a miss beside it is somebody else's session in the same
// tree: on this project's own board, the pre-release reviewer, a Codex
// session run in the checkout that never registers by design, turned `dibs
// doctor` red beside an agent whose every hook resolved. So an unresolved
// call is a misbinding only while some active agent in that directory could
// still be the caller.
//
// Two facts rule an agent out. A hook has already resolved to it
// (`reachedByHook`), so its session id is known to match. Or it holds a
// thread-shaped session id at all: a hook from its own session would quote
// that id and resolve, so a hook quoting another id is another session. The
// second matters because `reachedByHook` is per daemon lifetime and hooks
// fire at turn boundaries: with the first fact alone, `dibs upgrade` followed
// by the reviewer's first hook, while the seat was mid-turn, read "the guard
// is inert" for the length of that turn. What is left as a possible caller is
// an agent with no thread-shaped id, which is the bridge's `host-<ppid>`
// fallback or nothing at all, and that is the misbinding shape this counter
// exists to catch. An agent holding a thread id that is not its own (a nested
// bridge adopting its parent's) reads as reachable here; that case is the
// bridge's to prevent, and it does (mcpstdio_session.go).
func (e *Engine) hookStranger(cwd, host string) bool {
	for _, id := range e.state.ActiveAgentIDsOn(cwd, host) {
		if e.reachedByHook[id] {
			continue
		}
		if l := e.state.Agents[id]; l != nil && threadIDOf(l) != "" {
			continue
		}
		return false
	}
	return true
}

// noteHookFor records one lifecycle call against the agent it resolved to, if
// any, and classifies a miss with hookStranger. Only the engine loop calls it.
//
// The HOST comes with the directory, because both callers already resolved
// the hook by it and a directory means nothing without it. Round sixty-five
// of the pre-release review.
func (e *Engine) noteHookFor(kind string, l *core.Agent, cwd, host string) {
	if l != nil {
		if e.reachedByHook == nil {
			e.reachedByHook = map[string]bool{}
		}
		e.reachedByHook[l.ID] = true
		e.noteHook(kind, true, false)
		return
	}
	e.noteHook(kind, false, e.hookStranger(cwd, host))
}

// noteHook records one lifecycle call. `stranger` is consulted only when the
// call did not resolve: true means no agent that could be the caller is active
// in the directory the hook named, so the session is unregistered rather than
// misbound. See hookStranger.
func (e *Engine) noteHook(kind string, resolved, stranger bool) {
	e.hooks.lastAt.Store(time.Now().UnixNano())
	switch {
	case kind == "guard" && resolved:
		e.hooks.guardResolved.Add(1)
	case kind == "guard" && stranger:
		e.hooks.guardStrangers.Add(1)
	case kind == "guard":
		e.hooks.guardUnresolved.Add(1)
	case resolved:
		e.hooks.pollResolved.Add(1)
	case stranger:
		e.hooks.pollStrangers.Add(1)
	default:
		e.hooks.pollUnresolved.Add(1)
	}
}

// HookHealth reports whether the harness integrations are actually working.
func (e *Engine) HookHealth() HookHealth {
	h := HookHealth{
		GuardResolved:   e.hooks.guardResolved.Load(),
		GuardUnresolved: e.hooks.guardUnresolved.Load(),
		GuardStrangers:  e.hooks.guardStrangers.Load(),
		PollResolved:    e.hooks.pollResolved.Load(),
		PollUnresolved:  e.hooks.pollUnresolved.Load(),
		PollStrangers:   e.hooks.pollStrangers.Load(),
	}
	if ns := e.hooks.lastAt.Load(); ns > 0 {
		h.Last = time.Unix(0, ns)
	}
	total := h.GuardResolved + h.GuardUnresolved + h.PollResolved + h.PollUnresolved
	resolved := h.GuardResolved + h.PollResolved
	strangers := h.GuardStrangers + h.PollStrangers

	switch {
	case total == 0 && strangers > 0:
		// Every call so far came from a session nobody registered. The plugin
		// is installed and reaching the daemon; the agents in those sessions
		// have not taken a seat, so there is nothing to resolve TO. Not a
		// fault of the hooks, and not silence either.
		h.Verdict = "only-strangers"
		h.Hint = "harness hooks reach this daemon, but every call so far came from a " +
			"session whose agent never registered, so there was nobody to resolve " +
			"to. Those sessions edit unguarded and get no mail until their agent " +
			"calls register (a returning seat: same name and nonce)"
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
	case h.PollUnresolved > 0:
		// Counted since this file was written, and never once reported.
		//
		// The verdict asked "did ANY call resolve", which a machine running
		// several agents always answers yes to. So a board where one agent's
		// wake path was completely dead read `ok`, and `dibs doctor` printed
		// "harness hooks resolving", while nine consecutive polls for that
		// session found nobody and its mail sat unread for days. A count that
		// nothing reads is not a diagnostic, and this is the honesty rule
		// applied to the daemon's own health: a partial failure that reads as
		// success is worse than one that reads as nothing.
		h.Verdict = "poll-partly-unresolved"
		h.Hint = "some sessions are asking to be woken and this board cannot say " +
			"which agent they are: that agent's mail is never pushed into it, however " +
			"well the plugin is installed. It happens when an agent registers outside " +
			"its harness's MCP connection, so it carries no session id. Current " +
			"versions repair this on the agent's next call through the stdio bridge; " +
			"if it persists, that agent should re-register with the same name and " +
			"nonce from inside its harness"
	default:
		h.Verdict = "ok"
	}
	return h
}

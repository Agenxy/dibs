package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// A session that never registered is a stranger even in a directory where a
// registered agent works, once that agent's own hooks have resolved.
//
// The first discriminator was "is any agent active in the directory the hook
// named": none, and the caller is an unregistered session; one, and the hook
// carries a session id the agent did not register with. That second reading
// is only right when the active agent could be the caller. It is not when the
// agent's own hooks already resolve: then the one that did not is somebody
// else's session in the same tree. Measured on this project's own board on
// 2026-09-19: the pre-release reviewer, a Codex session run in the checkout
// that never registers by design, cost `dibs doctor` a "somebody's mail is
// not being delivered" beside an agent whose every hook resolved. A verdict
// that a reviewer can flip is one nobody reads.
func TestAStrangerBesideAReachedAgentIsStillAStranger(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	const dir = "/work/project"
	res, err := e.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: "seat", Nonce: "n-seat", SessionID: "seat-session",
		Agent: &core.AgentInfo{CWD: dir},
	})
	if err != nil {
		t.Fatalf("setup: register: %v", err)
	}
	if res["token"] == "" {
		t.Fatal("setup: no token")
	}

	// The seat's own hook resolves: it is reachable.
	if got, err := e.HookPoll(ctx, "seat-session", "Stop", dir, false, false); err != nil {
		t.Fatalf("hook_poll: %v", err)
	} else if got["agent"] != "seat" {
		t.Fatalf("setup: the seat's own hook resolved to %v, so nothing below is measured", got["agent"])
	}

	// A reviewer's session in the same directory, never registered.
	if _, err := e.HookPoll(ctx, "reviewer-session", "SessionStart", dir, false, false); err != nil {
		t.Fatalf("hook_poll: %v", err)
	}
	h := e.HookHealth()
	if h.PollUnresolved != 0 || h.PollStrangers != 1 {
		t.Errorf("an unregistered session beside a reachable agent was counted as a "+
			"misbinding: unresolved=%d strangers=%d", h.PollUnresolved, h.PollStrangers)
	}
	if h.Verdict != "ok" {
		t.Errorf("verdict = %q: a reviewer in the checkout flipped a healthy board to a fault", h.Verdict)
	}
}

// The fault shape stays a fault: an agent active in the directory whose OWN
// hooks have never resolved is the one that may be misbound, and a miss there
// still counts against the board.
func TestAMissBesideAnUnreachedAgentIsStillAFault(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	const dir = "/work/project"
	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: "seat", Nonce: "n-seat", SessionID: "host-4242",
		Agent: &core.AgentInfo{CWD: dir},
	}); err != nil {
		t.Fatalf("setup: register: %v", err)
	}
	// Somebody else resolved, so the verdict is not "never-resolved" by default.
	e.noteHook("poll", true, false)
	if _, err := e.HookPoll(ctx, "the-real-session-uuid", "Stop", dir, false, false); err != nil {
		t.Fatalf("hook_poll: %v", err)
	}
	h := e.HookHealth()
	if h.PollUnresolved != 1 || h.PollStrangers != 0 {
		t.Errorf("a miss beside an agent no hook has reached read as a stranger: "+
			"unresolved=%d strangers=%d", h.PollUnresolved, h.PollStrangers)
	}
	if h.Verdict != "poll-partly-unresolved" {
		t.Errorf("verdict = %q, wanted the fault", h.Verdict)
	}
}

// And still a stranger when the daemon has just started: an agent holding a
// thread-shaped session id is reachable by shape, before any hook of its own
// has arrived since the restart.
//
// `reachedByHook` is per daemon lifetime, and hooks fire at turn boundaries.
// So `dibs upgrade` followed by the reviewer's first hook, while the seat was
// mid-turn, read "hooks are reaching Dibs but resolving to NO agent: the
// guard is inert" for the length of that turn. Measured on this project's
// own board on 2026-09-19, one minute after the fix for the previous shape of
// the same false alarm was installed. A hook from the seat's own session
// would quote the thread id it holds, so a hook quoting another id is another
// session; only an agent with no thread-shaped id, which is the bridge's
// `host-<ppid>` fallback or nothing at all, can still be the misbound caller.
func TestAStrangerBesideAThreadBoundAgentIsAStrangerBeforeAnyHook(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	const dir = "/work/project"
	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: "seat", Nonce: "n-seat",
		SessionID: "0123abcd-4567-89ef-0123-456789abcdef",
		Agent:     &core.AgentInfo{CWD: dir},
	}); err != nil {
		t.Fatalf("setup: register: %v", err)
	}
	if _, err := e.HookPoll(ctx, "01a0bc56-e387-7310-bbf2-db64c2944687", "SessionStart", dir, false, false); err != nil {
		t.Fatalf("hook_poll: %v", err)
	}
	h := e.HookHealth()
	if h.PollStrangers != 1 || h.PollUnresolved != 0 {
		t.Errorf("a reviewer's first hook after a restart counted as a misbinding: "+
			"unresolved=%d strangers=%d", h.PollUnresolved, h.PollStrangers)
	}
	if h.Verdict != "only-strangers" {
		t.Errorf("verdict = %q: one unregistered session beside a thread-bound seat "+
			"read as an inert guard", h.Verdict)
	}
}

// An agent on ANOTHER machine, in a directory of the same name, is not a
// reason to call this machine's unregistered session a misbinding.
//
// The discriminator asks "could some active agent in that directory
// still be the caller". A directory is a path and a path is evidence on
// one computer, so /workspace/repo on two machines is two directories
// and an agent in the other one could not be the caller of anything
// here. The lookup compared the path alone, so a peer machine's agent
// holding the bridge's `host-<ppid>` fallback made every unregistered
// session on this one count as a fault: `dibs doctor` reported broken
// hooks across a fleet whose routing was correct, which is the verdict
// nobody can act on.
//
// Hook RESOLUTION has narrowed by host since round thirty-six
// (AgentForHookOn); the diagnostic beside it had the host in scope and
// threw it away. Round sixty-five of the pre-release review.
func TestAnAgentOnAnotherMachineIsNotAPossibleCallerHere(t *testing.T) {
	const dir, here, there = "/workspace/repo", "machine-a", "machine-b"
	st := core.NewState("test", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	e.SetHostID(here)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	// An agent on the OTHER machine, at the same path, holding the
	// bridge's fallback session id: the shape that reads as a possible
	// misbinding when nobody asks which machine it is on.
	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: "far", Nonce: "n-far", SessionID: "host-4242",
		Agent: &core.AgentInfo{CWD: dir, HostID: there},
	}); err != nil {
		t.Fatalf("setup: register: %v", err)
	}

	// An unregistered session HERE, in a directory of the same name.
	if _, err := e.HookPollFrom(ctx, "reviewer-session", "SessionStart", dir, here, false, false); err != nil {
		t.Fatalf("hook_poll: %v", err)
	}
	h := e.HookHealth()
	if h.PollUnresolved != 0 || h.PollStrangers != 1 {
		t.Errorf("an unregistered session was called a misbinding because an agent on "+
			"ANOTHER machine works at a path of the same name: unresolved=%d strangers=%d",
			h.PollUnresolved, h.PollStrangers)
	}
	// "only-strangers" is the honest verdict here: nothing has resolved
	// on this board yet, which is a different state from healthy. What
	// must NOT happen is a misbinding verdict, which is the one that
	// tells an operator their wake path is broken.
	if h.Verdict == "never-resolved" || strings.HasPrefix(h.Verdict, "poll-") {
		t.Errorf("verdict = %q: a correctly routed fleet reads as broken hooks because a "+
			"peer machine has a directory of the same name", h.Verdict)
	}

	// And the fault shape on THIS machine is still a fault, or the host
	// rule has simply switched the diagnostic off.
	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: "near", Nonce: "n-near", SessionID: "host-99",
		Agent: &core.AgentInfo{CWD: dir, HostID: here},
	}); err != nil {
		t.Fatalf("setup: register near: %v", err)
	}
	if _, err := e.HookPollFrom(ctx, "another-session", "SessionStart", dir, here, false, false); err != nil {
		t.Fatalf("hook_poll: %v", err)
	}
	if h := e.HookHealth(); h.PollUnresolved == 0 {
		t.Errorf("a miss beside an agent on THIS machine whose own hooks never resolved "+
			"stopped counting: unresolved=%d strangers=%d, and the diagnostic no longer "+
			"catches what it exists for", h.PollUnresolved, h.PollStrangers)
	}
}

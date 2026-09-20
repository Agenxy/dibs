package engine

import (
	"context"
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

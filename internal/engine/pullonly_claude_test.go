package engine

import (
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/peerwake"
)

// A Claude Code agent is not "missing a wake command": it is reached through
// its session socket by design (#255). The note said "No wake command is
// configured for claude code" about every one of them, and a sender read that
// as "this agent is pull-only" about an agent the daemon reached at once.
// Reported by k7-dev on 2026-09-30, after sending to an agent whose session
// received the notice within the second.
func TestAClaudeCodeAgentIsNotDescribedAsMissingAWakeCommand(t *testing.T) {
	const sid = "ab2bdbe2-3bc9-4f7b-8a1f-a638093a6256"
	e := New(core.NewState("test", core.DefaultLimits()), &memLedger{}, deadProber{})
	l := &core.Agent{
		ID: "cc", Name: "cc", Status: core.StatusDormant, SessionID: sid,
		Agent: &core.AgentInfo{Harness: "Claude Code", CWD: "/work"},
		Slots: map[string]core.Slot{},
	}
	e.state.Agents["cc"] = l
	e.peers.mu.Lock()
	e.peers.at = time.Now()
	e.peers.live = map[string]peerwake.Session{sid: {PID: 4242, SessionID: sid, CWD: "/work"}}
	e.peers.mu.Unlock()
	if !e.mightReachOverSocket(l) {
		t.Fatal("setup: the socket route does not see this agent")
	}
	n := e.PullOnlyNote(l)
	if strings.Contains(n, "No wake command is configured") {
		t.Errorf("a Claude Code agent was described as missing a wake command: %q", n)
	}
	if !strings.Contains(n, "session socket") {
		t.Errorf("the note does not say how a Claude Code agent is reached: %q", n)
	}
}

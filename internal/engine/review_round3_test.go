package engine

import (
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/peerwake"
)

// R3-5: a sender is not told nothing can wake an agent the daemon is about to nudge.
//
// wakeRoute tries the session socket when no command can run, and PullOnlyNote
// knew only about commands: it said "nothing on this board can wake" an agent
// whose socket was open and about to receive a notice. Best effort is still
// best effort, so the corrected wording promises an attempt and not an arrival.
// Found by the pre-release review, round three.
func TestTheNoteAdmitsABestEffortSocketWake(t *testing.T) {
	const sid = "ab2bdbe2-3bc9-4f7b-8a1f-a638093a6256"
	mk := func(status core.AgentStatus, withSocket bool) (*Engine, *core.Agent) {
		e := New(core.NewState("test", core.DefaultLimits()), &memLedger{}, deadProber{})
		l := &core.Agent{
			ID: "cc", Name: "cc", Status: status, SessionID: sid,
			Agent: &core.AgentInfo{Harness: "Claude Code", CWD: "/work"},
			Slots: map[string]core.Slot{},
		}
		e.state.Agents["cc"] = l
		e.peers.mu.Lock()
		e.peers.at = time.Now()
		e.peers.live = map[string]peerwake.Session{}
		if withSocket {
			e.peers.live[sid] = peerwake.Session{PID: 4242, SessionID: sid, CWD: "/work"}
		}
		e.peers.mu.Unlock()
		return e, l
	}
	for _, c := range []struct {
		name   string
		status core.AgentStatus
	}{{"active", core.StatusActive}, {"dormant", core.StatusDormant}} {
		t.Run(c.name+" with a socket", func(t *testing.T) {
			e, l := mk(c.status, true)
			if !e.mightReachOverSocket(l) {
				t.Fatal("setup: the socket route does not see this agent, so the note has nothing to admit")
			}
			n := e.PullOnlyNote(l)
			if !strings.Contains(n, "best-effort") || !strings.Contains(n, "Nothing can confirm") {
				t.Errorf("the note neither admits the attempt nor its limit: %q", n)
			}
			if strings.Contains(n, "nothing on this board can wake") || strings.Contains(n, "nothing is going to wake it") {
				t.Errorf("the note says nothing can wake an agent the daemon is about to nudge: %q", n)
			}
		})
		t.Run(c.name+" without a socket", func(t *testing.T) {
			e, l := mk(c.status, false)
			if n := e.PullOnlyNote(l); strings.Contains(n, "best-effort") {
				t.Errorf("promised a best-effort wake with no socket to try: %q", n)
			}
		})
	}
}

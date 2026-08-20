package engine

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/liveness"
)

// stall builds a verdict for a child that has done nothing since it started,
// the shape the real 7h39m stall had.
func stall() liveness.Verdict {
	return liveness.Verdict{
		State:  liveness.Stuck,
		Silent: 7*time.Hour + 39*time.Minute,
		Why:    "alive 7h39m and has used 110ms of CPU in all of it (0.0004% busy)",
	}
}

// makes the helper read as "a board with THIS agent on it" rather than a fixture
// with a hidden name.
//
//nolint:unparam // every caller wants "builder" today; the parameter is what
func agentOwning(t *testing.T, id string) *Engine {
	t.Helper()
	s := core.NewState("n1", core.DefaultLimits())
	res, _, err := s.Apply(&core.Op{
		Kind: core.OpRegister, Name: id, NewToken: "tok-" + id,
	}, time.Now())
	if err != nil {
		t.Fatalf("register %s: %v", id, err)
	}
	if got, _ := res["agent_id"].(string); got != id {
		t.Fatalf("agent registered as %q, not %q: the test's premise is wrong", got, id)
	}
	return &Engine{state: s}
}

// The report must reach the agent that spawned the child, and must say what
// Dibs did NOT do: a supervisor that reads as though it intervened invites a
// parent to assume the problem is handled.
func TestAStallIsReportedToTheSpaceThatSpawnedIt(t *testing.T) {
	e := agentOwning(t, "builder")
	a := liveness.Agent{PID: 48620, Harness: "codex", Owner: "builder", Via: "env"}

	if !e.reportStallLocked(a, stall(), "") {
		t.Fatal("nobody was told about a stalled child whose owner is on the board")
	}
	got := e.notices["builder"]
	if len(got) != 1 {
		t.Fatalf("expected one notice, got %d", len(got))
	}
	text := got[0].Text
	for _, want := range []string{"48620", "codex", "0.0004%", "your call", "dibs probe"} {
		if !strings.Contains(text, want) {
			t.Errorf("the report omits %q, which is what makes it actionable:\n  %s", want, text)
		}
	}
	if !strings.Contains(text, "has not touched it") {
		t.Error("the report does not say Dibs left the child alone: a parent reading it\n" +
			"  could reasonably assume the stall was already handled")
	}
}

// A child nobody can be shown to own is reported to NOBODY.
//
// The alternative (guessing) sends a stall report to an agent that cannot act
// on it while the one that can hears nothing, which is worse than the silence
// it replaces. It is still visible to a human through `dibs probe`.
func TestAnUnattributableStallIsNotMisdelivered(t *testing.T) {
	e := agentOwning(t, "builder")
	orphan := liveness.Agent{PID: 999, Harness: "codex", Owner: "", Via: ""}
	if e.reportStallLocked(orphan, stall(), "") {
		t.Error("an unattributable child was reported to somebody")
	}
	stranger := liveness.Agent{PID: 998, Harness: "codex", Owner: "some-other-session", Via: "session"}
	if e.reportStallLocked(stranger, stall(), "") {
		t.Error("a child owned by a session with no agent was reported anyway")
	}
	for agent, n := range e.notices {
		if len(n) > 0 {
			t.Errorf("agent %q received %d notices it has no business receiving", agent, len(n))
		}
	}
}

// Sleep is reported alongside, never folded into the silence. A parent told
// "silent for 41 minutes" after a lid was shut for 38 of them draws exactly the
// wrong conclusion.
func TestSleepIsReportedSeparatelyFromSilence(t *testing.T) {
	e := agentOwning(t, "builder")
	v := stall()
	v.Slept = 38 * time.Minute
	e.reportStallLocked(liveness.Agent{PID: 5, Harness: "claude", Owner: "builder"}, v, "")

	text := e.notices["builder"][0].Text
	if !strings.Contains(text, "slept 38m0s") || !strings.Contains(text, "NOT") {
		t.Errorf("the machine's sleep is not called out as excluded:\n  %s", text)
	}
}

// A stall report offers the way back, when there is one.
//
// Dibs does not restart anything: the parent knows what the child was for and
// whether re-running it is safe, and a supervisor that silently repairs teaches
// its operator nothing. But withholding the COMMAND is a different thing from
// declining to run it: a parent told "your subagent is stuck" and left to work
// out the incantation has been handed a problem instead of a decision.
func TestAStallReportOffersTheWayBack(t *testing.T) {
	e := agentOwning(t, "builder")
	a := liveness.Agent{PID: 900, Harness: "codex", Owner: "builder", Via: "env"}
	transcript := "/home/x/.codex/sessions/2026/06/08/" +
		"rollout-2026-06-08T08-03-00-019ea7c2-2c77-76a1-bde1-7635418cfb20.jsonl"

	e.reportStallLocked(a, stall(), transcript)
	text := e.notices["builder"][0].Text
	if !strings.Contains(text, "codex exec resume 019ea7c2-2c77-76a1-bde1-7635418cfb20") {
		t.Errorf("the report does not offer the resume command:\n  %s", text)
	}
	// And it still says Dibs did not act, or offering the command reads as
	// having run it.
	if !strings.Contains(text, "has not touched it") {
		t.Errorf("offering a resume lost the statement that Dibs left it alone:\n  %s", text)
	}

	// A harness with no resume, or no transcript, must not invent one.
	e.notices = nil
	e.reportStallLocked(liveness.Agent{PID: 901, Harness: "claude", Owner: "builder"}, stall(), transcript)
	if strings.Contains(e.notices["builder"][0].Text, "resume") {
		t.Error("offered a resume command for a harness that has none")
	}
	e.notices = nil
	e.reportStallLocked(a, stall(), "")
	if strings.Contains(e.notices["builder"][0].Text, "resume") {
		t.Error("offered a resume command with no transcript to derive one from")
	}
}

// A pid is evidence only on the machine that owns it.
//
// The prober asks THIS kernel about a pid. For an agent on another host the
// answer is not merely unavailable, it is wrong in both directions: nothing
// local holding that number marks a healthy remote agent dead and releases its
// claims, and any unrelated local process holding it reports the remote agent
// alive on evidence about a different program entirely.
//
// Armed the moment a fleet spans machines, because the stdio bridge registers
// with its OWN pid, which is a number on the client's machine.
func TestARemotePidIsNotProbedLocally(t *testing.T) {
	local, err := os.Hostname()
	if err != nil {
		t.Skip("no hostname on this machine")
	}

	// The remote host is DERIVED from the local one, never written as a
	// literal. This case used to name a machine, and the machine it named was
	// the author's own, so `ownsHost` answered "that is me" and correctly so:
	// the assertion that a remote pid is never probed failed on the single
	// machine this feature was written and dogfooded on, while passing on every
	// CI runner in the world, because no runner is called that. Green remote,
	// red local, from a fixture rather than the code.
	//
	// A test whose verdict depends on what somebody named their laptop is not
	// measuring the code. Appending to the local name is the one construction
	// that cannot be this host, whatever this host happens to be called.
	remote := local + "-not-this-machine"

	for _, tc := range []struct {
		name      string
		host      string
		wantProbe bool
	}{
		{name: "this machine", host: local, wantProbe: true},
		{name: "this machine, different case", host: strings.ToUpper(local), wantProbe: true},
		{name: "unknown host is treated as local", host: "", wantProbe: true},
		{name: "another machine is never probed", host: remote, wantProbe: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := &Engine{}
			a := &core.Agent{PID: 4242}
			if tc.host != "" {
				a.Agent = &core.AgentInfo{Host: tc.host}
			}

			if got := e.ownsHost(a); got != tc.wantProbe {
				t.Errorf("ownsHost(host=%q) = %v, want %v", tc.host, got, tc.wantProbe)
			}
		})
	}
}

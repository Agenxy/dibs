package engine

import (
	"testing"
	"time"

	"github.com/agenxy/lanes/internal/core"
)

// aliveProber answers "yes, that process is running" for everything.
type aliveProber struct{}

func (aliveProber) Alive(int) bool { return true }

func laneWithPID(t *testing.T, id string, pid int, prober Prober) (*Engine, *core.State) {
	t.Helper()
	s := core.NewState("n1", core.DefaultLimits())
	if _, _, err := s.Apply(&core.Op{
		Kind: core.OpRegisterLane, Name: id, NewToken: "tok-" + id, PID: pid,
	}, time.Now()); err != nil {
		t.Fatal(err)
	}
	e := New(s, nopLedger{}, prober)
	return e, s
}

// A healthy agent that has simply not spoken recently must not be reported as
// possibly hung.
//
// The board renders a stale lane whose process is alive as "stale (no contact)
// (hung?)". Because any lane reporting a PID was held to the 5-minute lane_ttl,
// every Claude Code lane on a live fleet accused itself within five minutes of
// its last tool call while its harness sat waiting for its human, and the
// operator had set idle_ttl to 45m specifically to stop that, only for it to be
// skipped because those lanes reported a PID.
//
// A PID is evidence about the PROCESS. For a harness-hosted agent the process
// outlives the turn by design, so it says nothing about whether the agent is
// working, and must not shorten the deadline for speaking.
func TestALiveProcessIsNotHeldToTheShortLease(t *testing.T) {
	e, s := laneWithPID(t, "k7b", 4242, aliveProber{})
	limits := s.Limits

	// Ten minutes of silence: past lane_ttl (5m), well inside idle_ttl (45m).
	quiet := time.Now().Add(-10 * time.Minute)
	s.Lanes["k7b"].LastCoordination = quiet
	e.seen["k7b"] = quiet

	e.sweep(time.Now())

	if got := s.Lanes["k7b"].Status; got != core.StatusActive {
		t.Fatalf("a live agent quiet for 10m was marked %q; lane_ttl=%v idle_ttl=%v",
			got, limits.LaneTTL, limits.IdleTTL)
	}
}

// Past idle_ttl, silence IS worth reporting: the fix must not make lanes
// immortal. At 45 minutes with a live process, "hung?" is a fair question.
func TestALiveProcessStillGoesStalePastIdleTTL(t *testing.T) {
	e, s := laneWithPID(t, "k7b", 4242, aliveProber{})

	quiet := time.Now().Add(-50 * time.Minute)
	s.Lanes["k7b"].LastCoordination = quiet
	e.seen["k7b"] = quiet

	e.sweep(time.Now())

	if got := s.Lanes["k7b"].Status; got == core.StatusActive {
		t.Fatal("silence past idle_ttl must still be reported; the lease is not decorative")
	}
}

// With no prober, a PID is a number nobody can check, and the clock is the only
// thing that will ever notice the agent is gone. That is the one case the short
// lease is for.
func TestAnUncheckablePIDKeepsTheShortLease(t *testing.T) {
	e, s := laneWithPID(t, "orphan", 4242, nil)

	quiet := time.Now().Add(-10 * time.Minute)
	s.Lanes["orphan"].LastCoordination = quiet
	e.seen["orphan"] = quiet

	e.sweep(time.Now())

	if got := s.Lanes["orphan"].Status; got == core.StatusActive {
		t.Fatal("a PID nobody can probe must still be judged by the short lease")
	}
}

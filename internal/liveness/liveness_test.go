package liveness

import (
	"strings"
	"testing"
	"time"
)

// at builds a sample n seconds into an observation window, with a separate
// wall clock so that system sleep can be expressed: awake advances Mono, slept
// advances only Wall.
type clock struct {
	wall time.Time
	mono time.Duration
}

func newClock() *clock {
	return &clock{wall: time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)}
}

// awake advances both clocks: the machine is running.
func (c *clock) awake(d time.Duration) { c.wall = c.wall.Add(d); c.mono += d }

// asleep advances ONLY the wall clock. This is what a closed lid does, and the
// reason both readings exist.
func (c *clock) asleep(d time.Duration) { c.wall = c.wall.Add(d) }

func (c *clock) sample(cpu time.Duration, bytes, tokens int64) Sample {
	return Sample{Wall: c.wall, Mono: c.mono, Alive: true, CPU: cpu, Bytes: bytes, Tokens: tokens}
}

// The measurement this package exists because of.
//
// Taken from a live `codex exec` at 15-second intervals: the middle window
// produced no bytes and no tokens, and the agent was entirely healthy — it was
// waiting on a model response. A detector that calls that "stuck" fires on
// every turn boundary, and a detector that cries wolf gets turned off.
func TestAHealthyAgentBetweenTurnsIsNotStuck(t *testing.T) {
	c := newClock()
	h := []Sample{c.sample(8300*time.Millisecond, 700773, 2514737)}
	c.awake(15 * time.Second)
	h = append(h, c.sample(8360*time.Millisecond, 700773, 2514737)) // flat output, CPU crept
	c.awake(15 * time.Second)
	h = append(h, c.sample(8500*time.Millisecond, 703752, 2684439)) // and back to work

	if v := Classify(h, DefaultConfig()); v.State != Working {
		t.Errorf("a real healthy agent was classified %q: %s\n"+
			"  this is the exact sample the package was built from — a flat 15s window\n"+
			"  mid-turn. Calling it anything but healthy makes the detector noise.",
			v.State, v.Why)
	}
}

// Flat output for a long time, but the process is burning CPU: a reasoning turn
// or a long tool call. Not stuck, and saying so would be a false alarm.
func TestOutputFlatButCPUBurningIsThinking(t *testing.T) {
	c := newClock()
	h := []Sample{c.sample(10*time.Second, 1000, 500)}
	cpu := 10 * time.Second
	// Deliberately PAST the Frozen threshold. An earlier version of this test
	// ran for four minutes, under the five-minute default — so it passed with
	// the CPU signal deleted entirely, because the "not yet frozen" branch
	// caught it. It was testing the clock, not the thing it was named for.
	// Found by deleting the CPU check and watching the suite stay green.
	for range 20 { // ten minutes of no output, CPU climbing throughout
		c.awake(30 * time.Second)
		cpu += 2 * time.Second
		h = append(h, c.sample(cpu, 1000, 500))
	}
	v := Classify(h, DefaultConfig())
	if v.State != Thinking {
		t.Errorf("got %q, want thinking: %s\n"+
			"  ten minutes without output but with CPU climbing is a long turn, not a\n"+
			"  stall — and CPU is the ONLY signal that separates them", v.State, v.Why)
	}
	if v.Silent < 9*time.Minute {
		t.Errorf("silence should reflect the whole flat run, got %s", v.Silent)
	}
}

// Alive, producing nothing, consuming nothing, for longer than the grace
// period. This is the case worth waking a parent for.
func TestAliveButProducingAndConsumingNothingIsStuck(t *testing.T) {
	c := newClock()
	h := []Sample{c.sample(10*time.Second, 1000, 500)}
	for range 14 { // seven minutes, past the five-minute default
		c.awake(30 * time.Second)
		h = append(h, c.sample(10*time.Second, 1000, 500)) // CPU frozen too
	}
	v := Classify(h, DefaultConfig())
	if v.State != Stuck {
		t.Fatalf("got %q, want stuck: %s", v.State, v.Why)
	}
	if v.Silent < 6*time.Minute {
		t.Errorf("silence understated: %s", v.Silent)
	}
}

// The failure this was written for.
//
// The lid closes, every subagent suspends, and on waking the parent sees forty
// minutes of nothing. Wall-clock silence says stuck. It was not stuck — it was
// not running, and killing it would destroy healthy work.
func TestASleepingMachineIsNotAStuckAgent(t *testing.T) {
	c := newClock()
	h := []Sample{c.sample(10*time.Second, 1000, 500)}
	c.awake(20 * time.Second)
	h = append(h, c.sample(11*time.Second, 1000, 500))

	c.asleep(40 * time.Minute) // lid shut: wall moves, monotonic does not
	h = append(h, c.sample(11*time.Second, 1000, 500))

	v := Classify(h, DefaultConfig())
	if v.State == Stuck {
		t.Errorf("a machine that slept 40 minutes was reported as a stuck agent: %s\n"+
			"  wall-clock silence and agent silence are different facts, and this is\n"+
			"  the one that makes a watchdog unusable on a laptop", v.Why)
	}
	if v.Slept < 39*time.Minute {
		t.Errorf("the sleep should be measured and reported, got %s", v.Slept)
	}
	if v.Silent > time.Minute {
		t.Errorf("silence must be AWAKE time (~20s), got %s — the sleep leaked into it", v.Silent)
	}
}

// And the reverse: sleep must not launder a genuine stall. An agent that was
// already frozen before the lid closed, and is still frozen well after it
// opened, is stuck — the sleep in the middle changes nothing about that.
func TestSleepDoesNotHideAGenuineStall(t *testing.T) {
	c := newClock()
	h := []Sample{c.sample(10*time.Second, 1000, 500)}
	for range 6 { // three awake minutes of nothing
		c.awake(30 * time.Second)
		h = append(h, c.sample(10*time.Second, 1000, 500))
	}
	c.asleep(30 * time.Minute)
	for range 8 { // four more awake minutes of nothing
		c.awake(30 * time.Second)
		h = append(h, c.sample(10*time.Second, 1000, 500))
	}
	v := Classify(h, DefaultConfig())
	if v.State != Stuck {
		t.Errorf("got %q, want stuck — seven AWAKE minutes of nothing is a stall "+
			"whether or not the machine slept in the middle: %s", v.State, v.Why)
	}
	if v.Slept < 29*time.Minute {
		t.Errorf("the sleep should still be reported alongside: %s", v.Slept)
	}
}

// A watchdog that assumes health before it has evidence is the bug it exists to
// prevent. One sample cannot show progress, because progress is a difference.
func TestNotEnoughEvidenceIsNotHealth(t *testing.T) {
	c := newClock()
	one := []Sample{c.sample(time.Second, 100, 10)}
	if v := Classify(one, DefaultConfig()); v.State != Unknown {
		t.Errorf("one sample gave %q; it cannot show a difference: %s", v.State, v.Why)
	}
	if v := Classify(nil, DefaultConfig()); v.State != Unknown {
		t.Errorf("no samples gave %q", v.State)
	}
	// Two samples, briefly flat: still too early to say anything. This is the
	// difference between a watchdog and a coin flip.
	c.awake(10 * time.Second)
	two := []Sample{one[0], c.sample(time.Second, 100, 10)}
	if v := Classify(two, DefaultConfig()); v.State == Stuck {
		t.Errorf("ten seconds of flatness was called stuck: %s", v.Why)
	}
}

// A dead process is not a slow one, and the distinction must not depend on how
// long anybody waited.
func TestAnExitedProcessIsReportedAsExited(t *testing.T) {
	c := newClock()
	h := []Sample{c.sample(time.Second, 100, 10)}
	c.awake(time.Minute)
	dead := c.sample(time.Second, 100, 10)
	dead.Alive = false
	if v := Classify(append(h, dead), DefaultConfig()); v.State != Exited {
		t.Errorf("got %q, want exited: %s", v.State, v.Why)
	}
}

// Tokens beat bytes where both exist: a transcript can grow because something
// was logged, but the token count grows only because the model produced
// something. An agent whose file is being appended to by a heartbeat, while the
// model has produced nothing for ten minutes, is not working.
func TestTokensAreTrustedOverBytes(t *testing.T) {
	c := newClock()
	h := []Sample{c.sample(10*time.Second, 1000, 500)}
	bytes := int64(1000)
	for range 16 { // eight minutes: file growing, tokens and CPU flat
		c.awake(30 * time.Second)
		bytes += 200
		h = append(h, c.sample(10*time.Second, bytes, 500))
	}
	if v := Classify(h, DefaultConfig()); v.State != Stuck {
		t.Errorf("got %q, want stuck — the file grew but the model produced nothing: %s",
			v.State, v.Why)
	}
}

// When no harness reports tokens, bytes are all there is, and they must still
// work. Falling back silently to "unknown" would make the detector useless for
// every harness that does not happen to write a usage record.
func TestBytesAloneAreEnoughWhenNoTokensAreReported(t *testing.T) {
	c := newClock()
	h := []Sample{c.sample(10*time.Second, 1000, 0)}
	c.awake(20 * time.Second)
	h = append(h, c.sample(11*time.Second, 4000, 0))
	if v := Classify(h, DefaultConfig()); v.State != Working {
		t.Errorf("got %q, want working from byte growth alone: %s", v.State, v.Why)
	}
}

// The verdict has to be usable by whoever reads it, which for this product is
// as often an agent as a person.
func TestEveryVerdictExplainsItself(t *testing.T) {
	c := newClock()
	h := []Sample{c.sample(time.Second, 1, 1)}
	for range 20 {
		c.awake(30 * time.Second)
		h = append(h, c.sample(time.Second, 1, 1))
	}
	for _, v := range []Verdict{
		Classify(nil, DefaultConfig()),
		Classify(h[:1], DefaultConfig()),
		Classify(h, DefaultConfig()),
	} {
		if v.Why == "" {
			t.Errorf("state %q came with no explanation", v.State)
		}
	}
}

// A process idle for its whole life is convictable from ONE look.
//
// The numbers are measured, not invented. Both processes were running on this
// machine at the same moment:
//
//	stalled codex exec   7h39m alive, 0.11s CPU   0.0004% busy
//	healthy codex exec     22m alive, 19.6s CPU   1.5%    busy
//
// Three orders of magnitude apart, which is why a threshold between them is
// safe. The parent of the stalled one had been blocked for over seven hours.
func TestALifetimeOfIdlenessIsConvictableAtAGlance(t *testing.T) {
	c := newClock()
	stalled := c.sample(110*time.Millisecond, 0, 0)
	stalled.Elapsed = 7*time.Hour + 39*time.Minute

	// ONE sample. This is the whole point: telling somebody who has already
	// waited seven hours to wait five more minutes is not an answer.
	v := Classify([]Sample{stalled}, DefaultConfig())
	if v.State != Stuck {
		t.Errorf("a process alive 7h39m on 0.11s of CPU was reported %q: %s\n"+
			"  this check existed, was unit-tested directly, and was never called from\n"+
			"  Classify — so the package could convict and the command could not", v.State, v.Why)
	}

	healthy := c.sample(19600*time.Millisecond, 100, 50)
	healthy.Elapsed = 22 * time.Minute
	if v := Classify([]Sample{healthy}, DefaultConfig()); v.State == Stuck {
		t.Errorf("a healthy agent at 1.5%% duty was convicted: %s", v.Why)
	}

	// A young process has not had time to prove anything, and killing a
	// subagent that simply has not started yet is the expensive mistake.
	young := c.sample(0, 0, 0)
	young.Elapsed = 30 * time.Second
	if v := Classify([]Sample{young}, DefaultConfig()); v.State == Stuck {
		t.Errorf("a 30-second-old process was convicted: %s", v.Why)
	}
}

// An "unknown" verdict must say what would settle it.
//
// Found on a cold install, following the README as a stranger would: a stalled
// stand-in spawned seconds earlier, probed, and answered "unknown — watched for
// only 9s". Correct, and a dead end. Somebody evaluating this stops there and
// concludes the feature does not work, because nothing told them the process
// was too young to judge or how long that lasts.
func TestAnUnknownVerdictNamesWhatWouldSettleIt(t *testing.T) {
	c := newClock()
	young := c.sample(0, 0, 0)
	young.Elapsed = 30 * time.Second
	c.awake(9 * time.Second)
	second := c.sample(0, 0, 0)
	second.Elapsed = 39 * time.Second

	v := Classify([]Sample{young, second}, DefaultConfig())
	if v.State != Unknown {
		t.Fatalf("got %q, want unknown for a 39-second-old silent process", v.State)
	}
	if !strings.Contains(v.Why, "min_age") {
		t.Errorf("the verdict does not name the flag that would settle it:\n  %s", v.Why)
	}
	if !strings.Contains(v.Why, "39s") {
		t.Errorf("the verdict does not say how old the process is, which is the fact\n"+
			"  that makes it unjudgeable:\n  %s", v.Why)
	}

	// Past the minimum age, silence is about the WATCH, and the advice changes
	// to match — naming --frozen rather than --min-age.
	old := c.sample(0, 0, 0)
	old.Elapsed = time.Hour
	older := c.sample(0, 0, 0)
	older.Elapsed = time.Hour
	cfg := DefaultConfig()
	cfg.MinDuty = 0 // suppress the duty-cycle rung so the watch branch is reached
	if v := Classify([]Sample{old, older}, cfg); strings.Contains(v.Why, "min_age") {
		t.Errorf("an old process was told to raise --min-age, which would not help:\n  %s", v.Why)
	}
}

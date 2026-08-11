// Package liveness answers how an agent process is doing, from outside it.
//
// The coarse question (is the process alive?) is Poller, and the engine's
// sweep already uses it to mark a lane whose PID has gone. This file is the
// finer one, and it is the question a parent agent cannot answer for itself:
// the subagent is still alive, so is it working, thinking, or stuck?
//
// A parent that spawns an out-of-process subagent. `codex exec`, another
// `claude`, an opencode run: gets exactly one signal back today, and it
// arrives at the end: an exit code. Everything before that is silence, and
// silence is ambiguous. The subagent may be mid-reasoning-turn, may be blocked
// on a socket that will never answer, may have been suspended when the lid
// closed. All three look identical from outside, and the parent waits.
//
// # What is actually observable
//
// Measured against a live `codex exec` (gpt-5.6, xhigh reasoning) at 15-second
// intervals:
//
//	12:07:00  bytes=700773  tokens=2514737  cpu=8.30s   both advanced
//	12:07:15  bytes=700773  tokens=2514737  cpu=8.36s   NEITHER advanced
//	12:07:30  bytes=703752  tokens=2684439  cpu=8.50s   both advanced
//
// The middle sample is the whole problem in one line. That agent was perfectly
// healthy (it was waiting on a model response) and for that window it emitted
// nothing at all. Any detector that calls a flat sample "stuck" will cry wolf
// on every turn boundary, and a detector that cries wolf gets ignored, which is
// worse than not having one.
//
// But CPU time advanced even then, because a streaming response costs cycles to
// parse. That is the distinction this package is built on:
//
//	output advancing                  -> Working
//	output flat, CPU advancing        -> Thinking   (a model turn; normal)
//	output flat, CPU flat, past grace -> Stuck      (blocked on something)
//	process gone                      -> Exited
//
// No single signal is decisive. A request that has been sent with nothing back
// yet burns no CPU either, which is why Stuck needs a grace period measured in
// minutes rather than seconds, and why that grace is a caller's decision.
//
// # Sleep is not silence
//
// The failure this was written for: a laptop closes, every subagent suspends,
// and on waking the parent sees forty minutes of nothing. Wall-clock silence
// says "stuck". It was not stuck; it was not running.
//
// So elapsed time is measured on a monotonic clock, which does not advance
// while the machine is asleep. On the machine this was developed on, 8.45 of
// the 80.3 hours since boot were sleep: a naive wall-clock detector would have
// reported every agent alive during those hours as hung.
//
// The correction is explicit rather than inherited from the runtime: a Sample
// carries both readings, Awake() returns the monotonic difference, and Slept()
// returns what the difference between them proves. A parent being told "your
// subagent has been silent for 3 awake minutes; the machine also slept for 38"
// can act on that. One told "silent for 41 minutes" cannot.
package liveness

import (
	"fmt"
	"time"
)

// State is what can honestly be said about a process from outside it.
type State string

const (
	// Working means the agent produced output since the previous sample.
	Working State = "working"
	// Thinking means no output, but the process is consuming CPU. A model turn in
	// progress, a long tool call, a file being parsed. Normal, and the single
	// most common reason a naive watchdog fires.
	Thinking State = "thinking"
	// Stuck means alive, but the agent has produced nothing and consumed nothing for longer
	// than the caller's grace period. Blocked on a socket, a lock, a prompt on
	// stdin nobody will answer.
	Stuck State = "stuck"
	// Exited means the process is gone. Whether that is success or failure is not
	// this package's business: the exit code is, and the caller has it.
	Exited State = "exited"
	// Unknown means not enough has been seen to say anything. Deliberately distinct
	// from Working; a watchdog that assumes health before it has evidence is
	// the bug it exists to prevent.
	Unknown State = "unknown"
)

// States is every state a caller may name. It lives beside the constants
// because the only way a caller can ask for one is by string, and a list kept
// in cmd/ drifts silently the moment a state is added here.
var States = []State{Working, Thinking, Stuck, Exited, Unknown}

// ParseState turns a caller's word into a State, reporting whether it was one
// at all.
//
// Nothing else in the tree converts string to State, and that is deliberate: an
// unrecognised value is not lenient input, it is a value no verdict can ever
// equal. `lanes probe --until exit` (for "exited") waited six hours in silence
// for a state that cannot occur, which is the worst shape a mistake can take,
// indistinguishable from the tool working and the agent simply never finishing.
func ParseState(s string) (State, bool) {
	for _, k := range States {
		if string(k) == s {
			return k, true
		}
	}
	return "", false
}

// Sample is one observation of an agent process.
//
// Wall and Mono are both recorded because their difference is information: it
// is the only way to tell "this agent stopped" from "this machine stopped".
type Sample struct {
	// Wall is the wall-clock reading, which advances during system sleep.
	Wall time.Time
	// Mono is a monotonic reading from a fixed base, which does not. Any
	// consistent base works; only differences are ever used.
	Mono time.Duration

	// Alive is whether the process existed at this instant.
	Alive bool
	// CPU is cumulative processor time consumed by the process. Monotonically
	// non-decreasing while alive.
	CPU time.Duration
	// Bytes is the size of the transcript the agent appends to. Every harness
	// worth watching writes one: codex to ~/.codex/sessions/**/rollout-*.jsonl,
	// Claude Code to ~/.claude/projects/**/<session>.jsonl.
	Bytes int64
	// Elapsed is how long the process has been alive. Known from a SINGLE
	// observation, which makes it the only signal that can convict without a
	// history: see the duty-cycle check in Classify.
	Elapsed time.Duration
	// Tokens is the agent's own cumulative token count, when the transcript
	// reports one, and 0 when it does not. The most semantically meaningful
	// progress signal available: bytes can grow because a log line was written,
	// but tokens grow only because the model produced something.
	Tokens int64
}

// Awake is the time between two samples that the machine was actually running.
func Awake(a, b Sample) time.Duration { return b.Mono - a.Mono }

// Slept is how much of the wall-clock interval between two samples the machine
// spent asleep. Zero (never negative) when the clocks agree.
//
// Floored at a second. The two clocks are read a few instructions apart, so
// they always disagree slightly, and reporting that as sleep produced "the
// machine slept 0s" under a rounded print: a true statement that reads like a
// bug and costs the reader their trust in the number beside it. Real sleep is
// never sub-second.
func Slept(a, b Sample) time.Duration {
	if d := b.Wall.Sub(a.Wall) - Awake(a, b); d >= time.Second {
		return d
	}
	return 0
}

// progressed reports whether real work happened between two observations.
//
// Tokens are preferred over bytes: a transcript can grow because something was
// logged, but a token count grows only because the model produced something. A
// child reporting its own counter lands in Tokens too, for harnesses whose
// stores cannot be read from outside.
func progressed(a, b Sample) bool {
	if a.Tokens > 0 || b.Tokens > 0 {
		return b.Tokens > a.Tokens
	}
	return b.Bytes > a.Bytes
}

// convictedByDutyCycle decides from one sample whether a process has been idle
// for its whole life.
//
// Deliberately conservative, and it must stay that way: the cost of a false
// "stuck" is a parent killing healthy work. The gap it exploits is enormous,
// measured, 0.0004% against 1.5%, so the threshold sits far from both, and the
// minimum age keeps it away from a young process that has genuinely not started
// yet. It returns "not sure" rather than "healthy": everything below still runs.
func convictedByDutyCycle(s Sample, cfg Config) (Verdict, bool) {
	if s.Elapsed < cfg.MinAge || s.Elapsed <= 0 {
		return Verdict{}, false
	}
	duty := s.CPU.Seconds() / s.Elapsed.Seconds()
	if duty > cfg.MinDuty {
		return Verdict{}, false
	}
	return Verdict{
		State:  Stuck,
		Silent: s.Elapsed,
		Why: fmt.Sprintf("alive %s and has used %s of CPU in all of it (%.4f%% busy). "+
			"it has done nothing since it started",
			round(s.Elapsed), round(s.CPU), duty*100),
	}, true
}

// Config is the caller's judgement about how patient to be.
//
// There are no universally correct values, and pretending otherwise is how a
// watchdog becomes noise. A reasoning model at high effort can spend minutes on
// one turn; a shell-heavy agent emits constantly. The defaults below are
// deliberately generous, because the cost of a false "stuck" is a parent
// killing healthy work, while the cost of a slow true positive is a few more
// minutes of waiting.
type Config struct {
	// Quiet is how long output may stay flat before the agent is no longer
	// called Working. Reaching it does not mean Stuck: only that the evidence
	// for Working has expired.
	Quiet time.Duration
	// Frozen is how long BOTH output and CPU may stay flat, in awake time,
	// before the verdict is Stuck. This is the number that matters, and the one
	// worth tuning per harness.
	Frozen time.Duration
	// MinAge is how long a process must have been alive before its duty cycle
	// is allowed to convict it. Below this it may simply not have started.
	MinAge time.Duration
	// MinDuty is the fraction of its life a process must have spent on the CPU
	// to escape that judgement. Far below any healthy agent and far above a
	// genuinely idle one; the gap between them is three orders of magnitude.
	MinDuty float64
}

// DefaultConfig is tuned from measurement, not taste: healthy 15-second flat
// windows were observed directly, so Quiet must be far above that, and Frozen
// far above Quiet. A model turn at high reasoning effort that sends a request
// and waits burns no CPU while it waits, which is the case Frozen must not
// misread.
func DefaultConfig() Config {
	return Config{
		Quiet: 90 * time.Second, Frozen: 5 * time.Minute,
		MinAge: 10 * time.Minute, MinDuty: 0.0005,
	}
}

// Verdict is the answer, with the evidence that produced it.
type Verdict struct {
	State State
	// Silent is how long, in AWAKE time, the agent has produced no output.
	Silent time.Duration
	// Slept is how much system sleep the observed window contained. Surfaced
	// separately so a parent is never told an agent was silent for time the
	// machine was not running.
	Slept time.Duration
	// Why is a sentence a person or an agent can act on.
	Why string
}

// Classify turns a series of observations into a verdict.
//
// history must be in time order, oldest first. It is a pure function of what
// was observed: it reads no clock and touches no process, so the awkward cases
// a machine that slept, a process that died between samples, a burst of
// output followed by a long wait: are all reachable in a test.
func Classify(history []Sample, cfg Config) Verdict {
	if len(history) == 0 {
		return Verdict{State: Unknown, Why: "nothing has been observed yet"}
	}
	last := history[len(history)-1]

	if !last.Alive {
		return Verdict{State: Exited, Why: "the process is no longer running"}
	}

	// Almost everything below needs two observations, because progress is a
	// difference. One thing does not: a process ALIVE for hours that has
	// consumed almost no processor time in all of it has done nothing since it
	// started, and that is visible the instant you look.
	//
	// Not a refinement. A real stalled `codex exec` on this machine had been up
	// 7h39m on 0.11s of CPU: a duty cycle of 0.0004%. A healthy one alongside
	// it ran at 1.5%, three orders of magnitude away. Without this, the honest
	// answer to "is my subagent stuck" was "wait five minutes and ask again",
	// which is the wrong thing to say to somebody who has already waited seven
	// hours.
	// Demonstrated progress beats a lifetime average, so this is checked BEFORE
	// the duty cycle. A process that idled for hours and has now started working
	// is working: convicting it on the strength of the hours would be exactly
	// wrong, and it is not hypothetical: a child that reports its own counter
	// (opencode) can show movement while its whole-life CPU share is still
	// negligible.
	if len(history) >= 2 && progressed(history[len(history)-2], last) {
		return Verdict{State: Working, Why: "producing output"}
	}
	if v, sure := convictedByDutyCycle(last, cfg); sure {
		return v
	}
	if len(history) < 2 {
		return Verdict{
			State: Unknown,
			Why:   "only one observation so far: progress is a difference, and there is nothing yet to differ from",
		}
	}

	lastProgress := 0
	for i := 1; i < len(history); i++ {
		if progressed(history[i-1], history[i]) {
			lastProgress = i
		}
	}
	silent := Awake(history[lastProgress], last)
	slept := Slept(history[lastProgress], last)

	if silent == 0 || lastProgress == len(history)-1 {
		return Verdict{
			State: Working, Slept: slept,
			Why: "producing output",
		}
	}

	// CPU is the finer signal, and the one that separates a model turn from a
	// block: a streaming response costs cycles to parse even while the
	// transcript has not yet grown.
	burning := last.CPU > history[lastProgress].CPU

	// Never claim more silence than was actually observed. If the window opens
	// at the first sample and nothing has ever advanced, the honest statement is
	// about the window, not about the agent's whole life.
	//
	// But burning CPU is POSITIVE evidence, not an absence of it, and an earlier
	// version checked the window before the CPU, so a healthy agent watched for
	// twenty seconds between turns came back "unknown" while visibly consuming
	// processor time. Correct, useless, and the most common way anybody will run
	// this: a parent asking once, now.
	if lastProgress == 0 && !progressed(history[0], history[1]) && silent < cfg.Quiet && !burning {
		return Verdict{
			State: Unknown, Silent: silent, Slept: slept,
			Why: tooEarly(silent, last.Elapsed, cfg),
		}
	}

	return verdictFor(silent, slept, burning, cfg)
}

// verdictFor turns the two measured facts: how long the agent has been silent,
// and whether it is still burning CPU: into the answer. Split out of Classify
// because that function had grown to hold both the evidence-gathering and the
// judgement, and the judgement is the part worth reading on its own.
func verdictFor(silent, slept time.Duration, burning bool, cfg Config) Verdict {
	switch {
	case silent < cfg.Quiet:
		return Verdict{
			State: Working, Silent: silent, Slept: slept,
			Why: fmt.Sprintf("last output %s ago, within the %s a normal turn takes",
				round(silent), round(cfg.Quiet)),
		}
	case burning:
		return Verdict{
			State: Thinking, Silent: silent, Slept: slept,
			Why: fmt.Sprintf("no output for %s, but still consuming CPU: a turn in progress, not a stall",
				round(silent)),
		}
	case silent < cfg.Frozen:
		return Verdict{
			State: Thinking, Silent: silent, Slept: slept,
			Why: fmt.Sprintf("no output and no CPU for %s; a request may be in flight, "+
				"so not called stuck until %s", round(silent), round(cfg.Frozen)),
		}
	default:
		why := fmt.Sprintf("no output and no CPU for %s: alive, but not doing anything",
			round(silent))
		if slept > 0 {
			why += fmt.Sprintf(" (the machine also slept %s, which is NOT counted in that)", round(slept))
		}
		return Verdict{State: Stuck, Silent: silent, Slept: slept, Why: why}
	}
}

// tooEarly explains an unknown verdict by naming what would settle it.
//
// Split out to keep Classify inside its complexity budget, and worth its own
// function anyway: a verdict of "unknown" with no route forward is where
// somebody testing this stops and concludes the feature does not work. Found on
// a cold install: a genuinely stalled stand-in, spawned seconds earlier,
// correctly unjudgeable and unhelpfully so.
func tooEarly(silent, elapsed time.Duration, cfg Config) string {
	why := fmt.Sprintf("watched for only %s, with no output and no CPU: too early to "+
		"distinguish a model turn from a stall", round(silent))
	if elapsed > 0 && elapsed < cfg.MinAge {
		return why + fmt.Sprintf(". This process is only %s old; a whole life of idleness is "+
			"convicted after %s: set [supervise] min_age in lanes.toml, or pass --min-age",
			round(elapsed), round(cfg.MinAge))
	}
	return why + fmt.Sprintf(". Keep watching: %s of continued silence makes it stuck "+
		"([supervise] frozen in lanes.toml, or --frozen)", round(cfg.Frozen))
}

// round trims a duration to something a person reads rather than parses.
func round(d time.Duration) time.Duration {
	switch {
	case d >= time.Hour:
		return d.Round(time.Minute)
	case d >= time.Minute:
		return d.Round(time.Second)
	default:
		return d.Round(100 * time.Millisecond)
	}
}

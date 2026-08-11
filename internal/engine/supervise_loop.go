package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/agenxy/lanes/internal/core"
	"github.com/agenxy/lanes/internal/liveness"
)

// superviseEvery is how often the machine is scanned for stalled subagents.
//
// Slow on purpose. A scan forks `ps` once for the table and once more per
// candidate, and the thing it is looking for takes minutes to become true,
// liveness.DefaultConfig() will not call anything stuck before five minutes of
// awake silence. Scanning every second would cost sixty times as much to learn
// the same fact sixty times later.
const superviseEvery = 20 * time.Second

// SuperviseSettings says how patient the sweep is and how often it looks.
type SuperviseSettings struct {
	liveness.Config
	Every time.Duration
}

// Supervise watches this machine's spawned agents and tells their owners when
// one stops working.
//
// Runs in its own goroutine, NOT on the writer loop: Discover() forks and reads
// process tables, and putting that on the single writer would stall every
// agent's coordination behind a process scan. Sampling and classification
// happen out here; only the verdict goes back through the loop.
//
// It reports and never acts. Killing or restarting a child is the parent's
// decision, made with context about what the child was for that this does not
// have, and even where Lanes could resume a codex session itself, a supervisor
// that silently repairs things teaches its operator nothing and hides a failure
// that may be systematic.
func (e *Engine) Supervise(ctx context.Context, s SuperviseSettings) {
	if s.Every <= 0 {
		s.Every = superviseEvery
	}
	e.superviseWith(ctx, s.Config, s.Every)
}

// superviseWith is Supervise with its two constants supplied.
//
// The thresholds were baked into the loop, which made the whole thing
// untestable without waiting out a ten-minute minimum age, so the sweep was
// the one part of supervision never exercised end to end. They are also worth
// tuning per machine: a laptop running one agent and a workstation running
// twelve want different patience.
func (e *Engine) superviseWith(ctx context.Context, cfg liveness.Config, every time.Duration) {
	history := map[int][]liveness.Sample{}
	told := map[int]bool{}
	tick := time.NewTicker(every)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			e.superviseOnce(ctx, cfg, history, told)
		}
	}
}

// superviseOnce is one pass: sample every agent process, classify it, and tell
// the owner about anything newly stuck.
func (e *Engine) superviseOnce(ctx context.Context, cfg liveness.Config,
	history map[int][]liveness.Sample, told map[int]bool,
) {
	found := liveness.Discover()
	live := make(map[int]bool, len(found))

	for _, a := range found {
		live[a.PID] = true
		// The transcript the child ANNOUNCED beats the one discovered by asking
		// its process which files it holds open. Same file when both work; the
		// announced one is right when the child is too wedged to hold it open.
		path := e.transcriptFor(ctx, a.Owner)
		if path == "" {
			path = liveness.FindTranscript(a.PID)
		}
		sample := liveness.Observe(a.PID, path)
		// A child that reports its own progress is believed when there is no
		// transcript to read. opencode keeps sessions in SQLite and shares one
		// log across every run on the machine, so there is no per-process file
		// to watch, but its plugin can count its own turns, and a counter that
		// only goes up is exactly what the classifier needs. Without this, an
		// opencode child is judged on CPU alone: a hard stall is caught, a slow
		// one is not.
		//
		// Only used when the transcript gave nothing, so a harness that has both
		// keeps the stronger signal.
		if sample.Tokens == 0 {
			if p := e.progressFor(ctx, a.Owner); p > 0 {
				sample.Tokens = p
			}
		}
		history[a.PID] = append(history[a.PID], sample)
		if len(history[a.PID]) > 64 {
			history[a.PID] = history[a.PID][32:]
		}

		v := liveness.Classify(history[a.PID], cfg)
		if v.State != liveness.Stuck {
			// Recovered. Clearing the flag means a child that stalls, resumes
			// and stalls again is reported both times: the second stall is
			// news, and suppressing it would be worse than the first silence.
			told[a.PID] = false
			continue
		}
		if told[a.PID] {
			continue // said once; repeating it every 20s is how a signal becomes noise
		}
		if e.reportStall(ctx, a, v, path) {
			told[a.PID] = true
		}
	}

	// Forget processes that are gone, or these maps grow for the life of the
	// daemon: a long-running board spawns a lot of short-lived agents.
	for pid := range history {
		if !live[pid] {
			delete(history, pid)
			delete(told, pid)
		}
	}
}

// transcriptFor returns the transcript a child announced through its own
// lifecycle hooks, if it announced one.
func (e *Engine) transcriptFor(ctx context.Context, owner string) string {
	if owner == "" {
		return ""
	}
	var path string
	_, _ = e.query(ctx, func() core.Result {
		if c, ok := e.children[owner]; ok {
			path = c.Transcript
		}
		return nil
	})
	return path
}

// progressFor returns the monotonic counter a child reported for itself, or 0.
func (e *Engine) progressFor(ctx context.Context, owner string) int64 {
	if owner == "" {
		return 0
	}
	var n int64
	_, _ = e.query(ctx, func() core.Result {
		if c, ok := e.children[owner]; ok {
			n = c.Progress
		}
		return nil
	})
	return n
}

// reportStall tells the owning lane, and returns whether anybody was told.
//
// Through the notice path rather than mail: a notice is precisely "something
// happened to you that you could not have inferred", it is delivered on the
// agent's next ack_board or hook_poll without it having to ask, and it needs no
// ledger op, which matters because a stall is an observation about this
// machine right now, not a coordination fact that must survive replay.
//
// An unattributable child is not reported to anybody. That is the deliberate
// choice: a wrong owner sends a stall report to an agent that cannot act on it
// while the one that can hears nothing, which is worse than the silence it
// replaces. It is visible to a human through `lanes probe` either way.
func (e *Engine) reportStall(ctx context.Context, a liveness.Agent, v liveness.Verdict, transcript string) bool {
	sent := false
	_, _ = e.query(ctx, func() core.Result {
		sent = e.reportStallLocked(a, v, transcript)
		return nil
	})
	return sent
}

// reportStallLocked is the decision, on the writer loop.
//
// Separated from its wrapper for the same reason noteChild is: query() sends on
// e.ops, which is nil on a zero-value Engine, so a test that called the wrapper
// would BLOCK rather than fail. That cost five minutes of CI earlier in this
// work, and the fix is structural rather than remembered.
func (e *Engine) reportStallLocked(a liveness.Agent, v liveness.Verdict, transcript string) bool {
	lane := e.laneForOwner(a.Owner)
	if lane == "" {
		return false
	}
	text := fmt.Sprintf(
		"A %s subagent you spawned (pid %d) has stopped working: %s. "+
			"Lanes has not touched it: restarting or abandoning it is your call, "+
			"and `lanes probe --pid %d` will show its current state.",
		a.Harness, a.PID, v.Why, a.PID)
	if v.Slept > 0 {
		text += fmt.Sprintf(" The machine also slept %s during this window, which is NOT "+
			"counted as silence.", v.Slept.Round(time.Second))
	}
	// Offer the command; do not run it. Withholding it is a different thing
	// from declining to run it: a parent told "your subagent is stuck" and
	// left to work out the incantation has been handed a problem instead of a
	// decision.
	if cmd := liveness.ResumeCommand(a.Harness, transcript); cmd != "" {
		text += fmt.Sprintf(" To pick it up where it stopped rather than starting over: `%s`.", cmd)
	}
	// Serial 0: this has no ledger op behind it, and claiming one would point a
	// reader at an entry that does not exist.
	e.pushNotice(lane, text, 0)
	return true
}

// laneForOwner resolves an attribution to a lane on the board.
//
// The owner may be a lane id already (the LANES_PARENT rung) or a harness
// session id (the session rungs), so both are tried. Called on the writer loop.
func (e *Engine) laneForOwner(owner string) string {
	if owner == "" || e.state == nil {
		return ""
	}
	if l, ok := e.state.Lanes[owner]; ok {
		return l.ID
	}
	if l := e.state.LaneForHook(owner, ""); l != nil {
		return l.ID
	}
	return ""
}

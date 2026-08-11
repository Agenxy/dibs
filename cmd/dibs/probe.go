package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/agenxy/dibs/internal/liveness"
	"github.com/agenxy/dibs/internal/paths"
)

// probe watches a spawned agent process and says whether it is working,
// thinking, stuck or gone.
//
// A parent that spawns an out-of-process subagent. `codex exec`, another
// `claude`, an opencode run: gets one signal back, at the end: an exit code.
// Until then the child's silence is ambiguous, and the three things it can mean
// (mid-turn, blocked forever, machine asleep) are indistinguishable from
// outside. So the parent waits, sometimes for hours, on work that stopped.
//
// This deliberately does NOT intervene. Dibs is a service agents pull from,
// never something that drives a harness: killing or restarting a child is the
// parent's decision, made with its own context about what the child was for.
// The job here is to end the silence.
//
// The default form is a single verdict, which is what a parent asks for when it
// wonders. --until is the one that matters: it BLOCKS until the child reaches a
// state worth waking for, then exits: the same shape as `dibs await`, so a
// parent can run it as a background task, keep working, and be woken by its own
// harness the moment the child stalls or dies. The shell watches; the model
// sleeps.
func probe(args []string) error {
	fs := flag.NewFlagSet("probe", flag.ContinueOnError)
	pid := fs.Int("pid", 0, "process id of the agent to watch (required)")
	transcript := fs.String("transcript", "",
		"session transcript the agent appends to; empty = discover the most recent one")
	until := fs.String("until", "",
		"block until the agent reaches one of these states (csv: stuck,exited), then exit")
	every := fs.Duration("every", 15*time.Second, "how often to sample")
	frozen := fs.Duration("frozen", 0,
		"no output AND no CPU for this long (awake time) before calling it stuck")
	// Zero means "not given", so the file can supply what the flag does not.
	// Defaulting these to the built-in numbers would make every invocation look
	// like an explicit override and silently beat agents.toml.
	quiet := fs.Duration("quiet", 0,
		"how long output may pause before the agent stops counting as working")
	minAge := fs.Duration("min-age", 0,
		"how old a process must be before a whole-life idleness verdict is allowed")
	minDuty := fs.Float64("min-duty", 0,
		"the fraction of its life a process must have spent on the CPU to escape "+
			"a whole-life idleness verdict")
	timeout := fs.Duration("timeout", 6*time.Hour, "give up watching after this long (exit 1)")
	asJSON := fs.Bool("json", false, "emit one JSON object per verdict instead of a line of prose")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	// No pid: list what is running. Somebody who suspects a stall does not
	// usually know the pid: asking them for one before answering "what is
	// running, and is any of it stuck" made the tool useless at the exact
	// moment it was wanted.
	if *pid <= 0 {
		return listAgents(settingsFor(*quiet, *frozen, *minAge, *minDuty), *asJSON)
	}

	path := *transcript
	if path == "" {
		path = liveness.FindTranscript(*pid)
	}
	// Say so rather than pretending. Without a transcript this can still tell
	// alive from dead and busy from frozen, but it cannot see the agent's own
	// output, and a caller who thinks it can would trust a weaker answer than it
	// is getting.
	if path == "" && !*asJSON {
		fmt.Fprintf(os.Stderr,
			"probe: no transcript found for pid %d: reporting on liveness and CPU only.\n"+
				"  Pass --transcript if you know the path; the agent's own token count is the\n"+
				"  strongest progress signal and this is running without it.\n", *pid)
	}

	cfg := settingsFor(*quiet, *frozen, *minAge, *minDuty)

	wake, err := parseUntil(*until)
	if err != nil {
		return err
	}

	var history []liveness.Sample
	deadline := time.Now().Add(*timeout)
	for {
		history = append(history, liveness.Observe(*pid, path))
		// Bounded: a six-hour watch at 15s is 1,440 samples, and only the run
		// back to the last progress is ever read. Keeping every sample would
		// grow without limit for no gain.
		if len(history) > 512 {
			history = history[len(history)-256:]
		}
		v := liveness.Classify(history, cfg)

		if len(wake) == 0 {
			// One-shot: answer the question that was asked. Progress is a
			// DIFFERENCE, so this cannot answer anything from a single sample,
			// an earlier version returned immediately and therefore always said
			// "unknown", which is a correct statement and a useless command.
			// Keep sampling until there is something to say.
			if v.State != liveness.Unknown || len(history) >= 4 {
				report(v, *pid, path, *asJSON)
				return nil
			}
		} else if wake[v.State] {
			// Watch mode: silent until the thing worth waking for happens, then
			// say it and exit, so the parent's harness delivers the news.
			report(v, *pid, path, *asJSON)
			return nil
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("gave up after %s; the agent was last %s", *timeout, v.State)
		}
		time.Sleep(*every)
	}
}

// listAgents reports every agent process on this machine.
//
// Discovery needs no cooperation from the children, so this works for a harness
// with no Dibs plugin at all, and for one that has stopped answering, which
// is the case that matters. Each is sampled twice, because progress is a
// difference and a single look cannot show one.
func listAgents(cfg liveness.Config, asJSON bool) error {
	found := liveness.Discover()
	if len(found) == 0 {
		if !asJSON {
			fmt.Println("no agent processes running.")
		}
		return nil
	}
	first := make(map[int]liveness.Sample, len(found))
	paths := make(map[int]string, len(found))
	for _, a := range found {
		paths[a.PID] = liveness.FindTranscript(a.PID)
		first[a.PID] = liveness.Observe(a.PID, paths[a.PID])
	}
	time.Sleep(2 * time.Second)

	for _, a := range found {
		v := liveness.Classify([]liveness.Sample{first[a.PID], liveness.Observe(a.PID, paths[a.PID])}, cfg)
		owner := a.Owner
		if owner == "" {
			owner = "unattributed"
		}
		if asJSON {
			out, _ := json.Marshal(map[string]any{
				"pid": a.PID, "harness": a.Harness, "owner": a.Owner,
				"via": a.Via, "state": string(v.State), "why": v.Why,
			})
			fmt.Println(string(out))
			continue
		}
		fmt.Printf("%-7d %-9s %-10s %-22s %s\n", a.PID, a.Harness, v.State, owner, v.Why)
	}
	return nil
}

// settingsFor layers the operator's configuration: defaults, then agents.toml,
// then any flags they typed.
//
// The file is read so ONE `[supervise]` table configures both the daemon's
// sweep and this command. They used to diverge: the table existed, the daemon
// honoured it, and `dibs probe` did not, so tuning it had no effect on the
// tool a person actually runs, and demonstrating detection meant spelling the
// configuration out loud on every invocation.
//
// Overlaid field by field rather than assigned wholesale, because a zero
// threshold is not "no threshold": it is "everything is stuck" or "nothing is",
// depending on the comparison, and that has silently disabled a check in this
// codebase before.
// parseUntil turns the --until list into the states that end the wait.
//
// A state nobody can reach is not a long wait, it is a wait that cannot end.
// Unvalidated, `--until exit` (for "exited") blocked for the whole six-hour
// timeout having printed nothing: indistinguishable from an agent that simply
// had not finished, which is the worst shape a typo can take.
func parseUntil(until string) (map[liveness.State]bool, error) {
	wake := map[liveness.State]bool{}
	for _, s := range strings.Split(until, ",") {
		if s = strings.TrimSpace(s); s == "" {
			continue
		}
		st, ok := liveness.ParseState(s)
		if !ok {
			names := make([]string, 0, len(liveness.States))
			for _, k := range liveness.States {
				names = append(names, string(k))
			}
			return nil, fmt.Errorf("--until %q is not a state; no verdict can ever equal it\n"+
				"  states are: %s", s, strings.Join(names, ", "))
		}
		wake[st] = true
	}
	return wake, nil
}

func settingsFor(quiet, frozen, minAge time.Duration, minDuty float64) liveness.Config {
	cfg := liveness.LoadSettings(paths.DataDir()).Apply(liveness.DefaultConfig())
	if quiet > 0 {
		cfg.Quiet = quiet
	}
	if frozen > 0 {
		cfg.Frozen = frozen
	}
	if minAge > 0 {
		cfg.MinAge = minAge
	}
	if minDuty > 0 {
		cfg.MinDuty = minDuty
	}
	return cfg
}

// report writes a verdict for whoever is reading: a person at a terminal or an
// agent parsing a background task's output.
func report(v liveness.Verdict, pid int, transcript string, asJSON bool) {
	if asJSON {
		out, _ := json.Marshal(map[string]any{
			"pid": pid, "state": string(v.State), "why": v.Why,
			"silent_seconds": v.Silent.Seconds(),
			"slept_seconds":  v.Slept.Seconds(),
			"transcript":     transcript,
		})
		fmt.Println(string(out))
		return
	}
	fmt.Printf("pid %d: %s. %s\n", pid, v.State, v.Why)
	// The sleep is called out separately because it is the single most likely
	// reason a parent is looking at this at all, and the one conclusion it must
	// not draw unaided: a suspended machine looks exactly like a hung agent.
	if v.Slept > 0 {
		fmt.Printf("  (the machine slept %s during this window; that time is not counted as silence)\n",
			v.Slept.Round(time.Second))
	}
}

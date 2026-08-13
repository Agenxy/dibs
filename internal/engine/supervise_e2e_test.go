package engine

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/liveness"
)

// The whole chain, against a real process: spawn a stalled agent, sweep the
// machine, and assert the agent that spawned it was told.
//
// Every other test here covers one link. This is the only one that proves they
// join up, and it existed as a gap for a while precisely because it was awkward:
// the sweep needs a process that LOOKS like an agent to the discovery pass,
// CARRIES the parent stamp in a readable environment, and does nothing at all.
//
// The subject is compiled here rather than faked with system tools. That is not
// fastidiousness: macOS hides the environment of Apple-signed platform binaries,
// so /bin/sleep and anything /bin/bash runs are exactly the processes this
// cannot read. Three earlier attempts to fake an agent that way produced a
// confident false negative. A user-compiled binary named `codex` is what a real
// harness is.
func TestAStalledAgentReachesTheSpaceThatSpawnedIt(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go toolchain to build the stand-in agent")
	}
	dir := t.TempDir()
	fake := filepath.Join(dir, "codex")
	src := filepath.Join(dir, "main.go")
	// Blocks forever, consuming nothing: alive, silent, and burning no CPU,
	// which is exactly the shape of the 7h39m stall this was built for.
	//
	// NOT `select {}`: Go's runtime detects that as a deadlock and kills the
	// process on the spot. It then lingered as an unreaped zombie, which still
	// has an elapsed time, so the wait-for-age loop below passed while discovery
	// had quietly stopped matching it. A stand-in that dies at birth tests
	// nothing and looks like a broken sweep.
	prog := "package main\n\nimport \"time\"\n\nfunc main() {\n\tfor {\n\t\ttime.Sleep(time.Hour)\n\t}\n}\n"
	if err := os.WriteFile(src, []byte(prog), 0o600); err != nil {
		t.Fatal(err)
	}
	build := exec.Command("go", "build", "-o", fake, src)
	build.Env = append(os.Environ(), "GOFLAGS=")
	if out, err := build.CombinedOutput(); err != nil {
		t.Skipf("could not build the stand-in agent: %v\n%s", err, out)
	}

	// Spawned the way the PreToolUse stamp spawns one: the parent's agent in the
	// environment, inherited by the process and everything below it.
	child := exec.Command(fake, "exec", "--stand-in")
	child.Env = append(os.Environ(), "DIBS_PARENT=builder")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = child.Process.Kill(); _ = child.Wait() }()

	// Discovery must see it at all, or nothing downstream can run. Asserted
	// separately so a failure here is not read as a failure of the report.
	var mine *liveness.Agent
	for _, a := range liveness.Discover() {
		if a.PID == child.Process.Pid {
			mine = &a
			break
		}
	}
	if mine == nil {
		t.Fatalf("the sweep did not discover pid %d running %q: a stalled agent that is\n"+
			"  never found is never reported, whatever the rest of the chain does",
			child.Process.Pid, fake+" exec")
	}
	if mine.Owner != "builder" || mine.Via != "env" {
		t.Fatalf("discovered but attributed owner=%q via=%q, want builder via env:\n"+
			"  the stamp is the deterministic rung and this is the round trip that proves it",
			mine.Owner, mine.Via)
	}

	// A board with that agent on it, and a sweep impatient enough to finish
	// inside a test. Only the thresholds are relaxed; the logic is the shipped
	// logic.
	s := core.NewState("n1", core.DefaultLimits())
	if _, _, err := s.Apply(&core.Op{
		Kind: core.OpRegister, Name: "builder", NewToken: "tok",
	}, time.Now()); err != nil {
		t.Fatal(err)
	}
	e := &Engine{state: s, ops: make(chan request, 4)}

	// `ps` reports elapsed time to the SECOND, so a just-spawned process reads
	// as zero seconds old and cannot be convicted of having idled its whole
	// life: the duty-cycle rung needs an age to divide by. Wait for the
	// process to be observably old enough rather than sleeping a guessed
	// interval: the condition is the thing being waited on.
	deadline := time.Now().Add(20 * time.Second)
	for {
		if s := liveness.Observe(child.Process.Pid, ""); s.Alive && s.Elapsed >= 2*time.Second {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the stand-in agent never reported an age; ps is not answering as expected")
		}
	}

	cfg := liveness.DefaultConfig()
	cfg.MinAge = time.Second
	// The timescale is compressed, so the threshold is scaled with it: the
	// LOGIC is the shipped logic. Any process burns some CPU starting up, and
	// over a two-second life that fixed cost is ~0.5% of it, far above the
	// 0.05% that means "did nothing for seven hours". Both are the same
	// judgement about the same shape; only the window differs. Leaving the
	// shipped threshold here would fail the test for a correct reason and teach
	// nothing.
	cfg.MinDuty = 0.05

	// superviseOnce hands its verdict back through the writer loop, so one has
	// to be running to receive it.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case req := <-e.ops:
				req.reply <- reply{req.fn(), nil}
			}
		}
	}()

	// Isolate the delivery link before running the sweep, so a failure names
	// which of the four links broke instead of only the last one.
	v := liveness.Classify([]liveness.Sample{liveness.Observe(child.Process.Pid, "")}, cfg)
	if v.State != liveness.Stuck {
		t.Fatalf("classifier: %s. %s", v.State, v.Why)
	}
	if !e.reportStallLocked(*mine, v, "") {
		t.Fatalf("reportStallLocked declined to deliver for owner=%q; agentForOwner said %q",
			mine.Owner, e.agentForOwner(mine.Owner))
	}
	e.notices = nil // reset; the sweep must do this itself
	e.superviseOnce(ctx, cfg, map[int][]liveness.Sample{}, map[int]bool{})

	got := e.notices["builder"]
	if len(got) == 0 {
		// Report what the classifier actually decided, or a failure here is a
		// guessing game between four links that each look fine alone.
		v := liveness.Classify([]liveness.Sample{liveness.Observe(child.Process.Pid, "")}, cfg)
		t.Fatalf("the sweep found and attributed a stalled agent and told nobody.\n"+
			"  classifier says: %s. %s", v.State, v.Why)
	}
	if !strings.Contains(got[0].Text, "has stopped working") {
		t.Errorf("the notice does not say what happened: %q", got[0].Text)
	}
	if !strings.Contains(got[0].Text, "has not touched it") {
		t.Errorf("the notice does not say Dibs left it alone, so a parent could\n"+
			"  reasonably assume the stall was handled: %q", got[0].Text)
	}

	// A reported counter reaches the classifier when there is no transcript.
	//
	// This stand-in has none, it is a bare binary, not a harness, which is
	// exactly the opencode situation. Verifies the field is actually read: a
	// counter nothing consumes is this codebase's recurring bug, and it has
	// shipped that shape more than once.
	//
	// The first sweep is a BASELINE and is expected to convict: progress is a
	// difference, and one observation cannot show one. Movement only becomes
	// visible from the second sweep on, which is what the classifier needs and
	// what a real harness provides.
	hist := map[int][]liveness.Sample{}
	e.noteChild(Child{SessionID: "builder", Progress: 1, State: "running"}, time.Now())
	e.superviseOnce(ctx, cfg, hist, map[int]bool{}) // baseline
	e.notices = nil

	for i := int64(2); i <= 4; i++ {
		e.noteChild(Child{SessionID: "builder", Progress: i, State: "running"}, time.Now())
		e.superviseOnce(ctx, cfg, hist, map[int]bool{})
	}
	if n := len(e.notices["builder"]); n != 0 {
		t.Errorf("a child reporting RISING progress was reported stalled (%d notices)\n"+
			"  the counter is not reaching the classifier, so a harness whose store\n"+
			"  Dibs cannot read is judged on CPU alone", n)
	}

	// And when the counter stops moving, the verdict returns. Otherwise the
	// mechanism would be a way for a wedged child to look healthy forever.
	for range 3 {
		e.superviseOnce(ctx, cfg, hist, map[int]bool{})
	}
	if len(e.notices["builder"]) == 0 {
		t.Error("a child whose counter stopped moving was never reported")
	}

	// Said once. A report repeated every sweep is how a signal becomes noise.
	//
	// Its own baseline rather than the one captured above, because the progress
	// checks in between deliberately cleared the notices: a shared baseline
	// would make this assert about that clearing instead of about repetition.
	e.notices = nil
	told := map[int]bool{child.Process.Pid: true}
	for range 3 {
		e.superviseOnce(ctx, cfg, map[int][]liveness.Sample{}, told)
	}
	if n := len(e.notices["builder"]); n != 0 {
		t.Errorf("a stall already marked as reported was reported %d more times", n)
	}
}

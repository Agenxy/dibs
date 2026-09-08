package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// The fallback runs when the primary fails, and only then.
//
// One harness, two states, a command for each: `codex exec resume` starts a
// CLOSED thread and refuses one that is open in the desktop app; `codex queue`
// delivers into an OPEN thread and parks silently on a closed one. Neither
// alone reaches every agent, and the desktop-app case was reported as
// unreachable for weeks because only the first was ever configured.
//
// Asserted with a relative touch, the same way the directory test does it: a
// marker that exists proves which command ran, and one that does not proves
// which did not.
func TestTheFallbackRunsOnlyWhenThePrimaryFails(t *testing.T) {
	if _, err := os.Stat("/usr/bin/touch"); err != nil {
		t.Skip("no /usr/bin/touch on this platform")
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Run("primary finds the thread open, fallback delivers", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("DIBS_TEST_OPEN_THREAD", "1")
		ok := runWakeCommands([]string{self, "-test.run=TestHelperPrimaryFindsTheThreadOpen"},
			[]string{"/usr/bin/touch", "fallback-ran"}, "somebody", dir, 10*time.Second, time.Second)
		if !ok {
			t.Fatal("the wake was reported as failed although the fallback exited 0: " +
				"an agent whose thread is open in its desktop app is reachable by " +
				"exactly that command and by nothing else")
		}
		if _, err := os.Stat(filepath.Join(dir, "fallback-ran")); err != nil {
			t.Error("reported success without the fallback having run")
		}
	})
	t.Run("primary delivers, fallback is not touched", func(t *testing.T) {
		dir := t.TempDir()
		ok := runWakeCommands([]string{"/usr/bin/true"}, []string{"/usr/bin/touch", "fallback-ran"},
			"somebody", dir, 10*time.Second, time.Second)
		if !ok {
			t.Fatal("a primary that exited 0 was reported as a failed wake")
		}
		if _, err := os.Stat(filepath.Join(dir, "fallback-ran")); err == nil {
			t.Error("the fallback ran after the primary had already delivered: for " +
				"codex that is a second message into a thread that just got one")
		}
	})
	t.Run("primary fails for another reason, fallback is not run", func(t *testing.T) {
		dir := t.TempDir()
		ok := runWakeCommands([]string{"/usr/bin/false"}, []string{"/usr/bin/touch", "fallback-ran"},
			"somebody", dir, 10*time.Second, time.Second)
		if ok {
			t.Fatal("a primary that failed for a reason that is not an open thread was reported " +
				"as a wake through the fallback: for codex that parks the message and cancels the retry")
		}
		if _, err := os.Stat(filepath.Join(dir, "fallback-ran")); err == nil {
			t.Error("the fallback ran although the primary said nothing about an open thread")
		}
	})
	t.Run("no fallback configured is the old behaviour", func(t *testing.T) {
		if runWakeCommands([]string{"/usr/bin/false"}, nil, "somebody", t.TempDir(),
			10*time.Second, time.Second) {
			t.Error("a failed primary with no fallback was reported as a wake")
		}
	})
}

// The plan carries the fallback, substituted the same way as the primary.
//
// The first version of the directory fix set cwd on one branch of wakeFor and
// not the other, and looked used. This is the same trap one field over: a
// fallback that is stored, validated and never copied into the plan is a
// feature that exists everywhere except where the process starts.
func TestThePlanCarriesTheSubstitutedFallback(t *testing.T) {
	e := New(core.NewState("test", core.DefaultLimits()), &memLedger{}, deadProber{})
	e.SetWakeCommands(map[string]WakeCommand{
		"codex": {
			Argv:     []string{"/bin/echo", "resume", "{thread}"},
			Fallback: []string{"/bin/echo", "queue", "{thread}", "{message}"},
		},
	})
	const thread = "0199a0b1-c2d3-4e5f-8a9b-0c1d2e3f4a5b"
	l := &core.Agent{
		ID: "worker", Name: "worker", Status: core.StatusDormant, SessionID: thread,
		Agent: &core.AgentInfo{Harness: "Codex", CWD: "/work"}, Slots: map[string]core.Slot{},
	}
	e.state.Agents["worker"] = l
	plan, ok := e.wakeFor(l, core.MsgQuestion, core.Event{
		Type: "message.sent", Agent: "asker", To: "worker",
		Data: map[string]any{"msg_type": core.MsgQuestion, "from": "asker"},
	})
	if !ok || len(plan.argv) == 0 {
		t.Fatal("no command plan was made, so this proves nothing about the fallback")
	}
	if len(plan.fallback) != 4 || plan.fallback[2] != thread || plan.fallback[3] != wakeNotice {
		t.Errorf("the plan's fallback is %q: the thread and the notice were not "+
			"substituted, so the command that reaches an open desktop-app thread "+
			"would run with literal placeholders", plan.fallback)
	}
}

// TestHelperPrimaryFindsTheThreadOpen is the primary wake command for the test
// above when run as a child: it says what codex says of a thread the desktop
// app holds open, and exits 1. In the parent it does nothing.
func TestHelperPrimaryFindsTheThreadOpen(t *testing.T) {
	if os.Getenv("DIBS_TEST_OPEN_THREAD") != "1" {
		return
	}
	fmt.Fprintln(os.Stderr, "thread-store conflict: thread 019ffe52 already has an active writer")
	os.Exit(1)
}

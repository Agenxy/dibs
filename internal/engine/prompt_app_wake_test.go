package engine

import (
	"os"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/harnessenv"
)

// Actual planner -> queue command -> app open. Presence is explicitly active
// and no test calls an open setter. Only the app contact is replaced.
func TestPromptAppWakeReachesADormantThreadWhileThePersonIsActive(t *testing.T) {
	if _, err := os.Stat("/usr/bin/true"); err != nil {
		t.Skip("no /usr/bin/true on this platform")
	}
	for _, loaded := range []bool{false, true} {
		t.Run(map[bool]string{false: "dormant", true: "loaded"}[loaded], func(t *testing.T) {
			t.Setenv("DIBS_DIR", t.TempDir())
			app := &fakeApp{holds: loaded}
			app.install(t)
			shower.Away = func() (bool, bool) { return false, true }
			shower.Idle = func() (time.Duration, bool) { return 0, true }
			shower.MinIdle = 10 * time.Minute
			release := make(chan struct{})
			t.Cleanup(func() { close(release) })
			shower.Wait = func(time.Duration) { <-release }
			e := New(core.NewState("test", core.DefaultLimits()), &memLedger{}, deadProber{})
			e.SetWakeCommands(map[string]WakeCommand{"codex": {Argv: []string{"/usr/bin/true", "queue", "{thread}"}}})
			worker := &core.Agent{
				ID: "prompt-worker", Status: core.StatusDormant, SessionID: "0199a0b1-c2d3-4e5f-8a9b-0c1d2e3f4a6c",
				Agent: &core.AgentInfo{Harness: "Codex", CWD: t.TempDir(), Surface: harnessenv.ChatGPTApp},
				Slots: map[string]core.Slot{},
			}
			e.state.Agents[worker.ID] = worker
			plan, ok := e.wakeFor(worker, core.MsgQuestion, questionFor(worker.ID))
			if !ok || len(plan.argv) == 0 || !e.runWake(plan, worker.ID) {
				t.Fatal("setup: actual command wake did not succeed")
			}
			want := 1
			if loaded {
				want = 0
			}
			if len(app.opened) != want {
				t.Fatalf("loaded=%v: opens=%q, want %d", loaded, app.opened, want)
			}
			if want == 1 && (len(app.opened[0]) != 3 || app.opened[0][1] != "-g") {
				t.Fatalf("prompt wake did not use background open: %q", app.opened)
			}
		})
	}
}

// #304/7428955's false unloaded result must not switch the app on every
// successfully queued message, even when visibility stays false after open.
func TestRapidSuccessfulCommandWakesOpenAThreadOnlyOnce(t *testing.T) {
	if _, err := os.Stat("/usr/bin/true"); err != nil {
		t.Skip("no /usr/bin/true on this platform")
	}
	t.Setenv("DIBS_DIR", t.TempDir())
	app := &fakeApp{}
	app.install(t)
	shower.Away = func() (bool, bool) { return true, true }
	e := New(core.NewState("test", core.DefaultLimits()), &memLedger{}, deadProber{})
	e.SetWakeCommands(map[string]WakeCommand{"codex": {Argv: []string{"/usr/bin/true", "queue", "{thread}"}}})
	worker := &core.Agent{
		ID: "rapid-worker", Status: core.StatusDormant, SessionID: "0199a0b1-c2d3-4e5f-8a9b-0c1d2e3f4a6d",
		Agent: &core.AgentInfo{Harness: "Codex", CWD: t.TempDir(), Surface: harnessenv.ChatGPTApp},
		Slots: map[string]core.Slot{},
	}
	e.state.Agents[worker.ID] = worker
	plan, ok := e.wakeFor(worker, core.MsgQuestion, questionFor(worker.ID))
	if !ok || len(plan.argv) == 0 {
		t.Fatal("setup: planner made no command wake")
	}
	for range 20 {
		if !e.runWake(plan, worker.ID) {
			t.Fatal("setup: successful command was reported failing")
		}
	}
	if len(app.opened) != 1 {
		t.Fatalf("twenty queued wakes produced %d opens, want one", len(app.opened))
	}
}

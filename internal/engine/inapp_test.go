package engine

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/harnessenv"
)

// fakeApp stands in for the ChatGPT app: whether it holds the thread, and
// every open it was asked for.
type fakeApp struct {
	holds  bool
	opened [][]string
}

func (a *fakeApp) install(t *testing.T) {
	t.Helper()
	// Never the operator's own threads: an empty Codex home, unless a test
	// writes a transcript into it.
	if os.Getenv("DIBS_TEST_CODEX_HOME_SET") == "" {
		t.Setenv("CODEX_HOME", t.TempDir())
	}
	prev := shower
	shower = harnessenv.Shower{
		Holds: func(string) bool { return a.holds },
		Open:  func(argv []string) error { a.opened = append(a.opened, argv); return nil },
	}
	t.Cleanup(func() { shower = prev })
}

// A wake reaches an agent IN the app it runs in, and nowhere else.
//
// The operator found their ChatGPT threads running in a headless Codex Dibs
// had started, and drew the line: an agent is woken in the app it started in,
// launching the app if it must, and never in another environment. `codex
// queue` alone was not enough, because the app delivers a queued message only
// to a thread it has loaded, and after an app update it had loaded none of
// them, so the message sat in the queue until somebody happened to open the
// thread. Each case below is one half of that rule.
//
// Driven through wakeFor and runWake, the two halves production uses, with
// only the app itself replaced: a test that reached the real `open` would
// switch the operator's ChatGPT window every time the suite ran.
func TestAWakeOpensTheThreadInTheAppTheAgentRunsIn(t *testing.T) {
	if _, err := os.Stat("/usr/bin/true"); err != nil {
		t.Skip("no /usr/bin/true on this platform")
	}
	const thread = "0199a0b1-c2d3-4e5f-8a9b-0c1d2e3f4a5b"
	wake := func(t *testing.T, app *fakeApp, surface, command string) bool {
		t.Helper()
		app.install(t)
		e := New(core.NewState("test", core.DefaultLimits()), &memLedger{}, deadProber{})
		e.SetWakeCommands(map[string]WakeCommand{
			"codex": {Argv: []string{command, "queue", "--thread", "{thread}"}},
		})
		l := &core.Agent{
			ID: "worker", Name: "worker", Status: core.StatusDormant, SessionID: thread,
			Agent: &core.AgentInfo{Harness: "Codex", CWD: t.TempDir(), Surface: surface},
			Slots: map[string]core.Slot{},
		}
		e.state.Agents["worker"] = l
		plan, ok := e.wakeFor(l, core.MsgQuestion, core.Event{
			Type: "message.sent", Agent: "asker", To: "worker",
			Data: map[string]any{"msg_type": core.MsgQuestion, "from": "asker"},
		})
		if !ok || len(plan.argv) == 0 {
			t.Fatal("setup: no command plan was made, so this proves nothing")
		}
		return e.runWake(plan, "worker")
	}

	t.Run("the app is not holding the thread: it is opened there", func(t *testing.T) {
		app := &fakeApp{}
		if !wake(t, app, harnessenv.ChatGPTApp, "/usr/bin/true") {
			t.Fatal("setup: the queue command was reported as failing")
		}
		if len(app.opened) != 1 || len(app.opened[0]) != 2 || app.opened[0][1] != "codex://threads/"+thread {
			t.Fatalf("asked the app for %q: the message is queued for a thread the app has "+
				"not loaded, and nobody can see the agent act on it", app.opened)
		}
	})
	t.Run("the app already holds the thread: it is left alone", func(t *testing.T) {
		app := &fakeApp{holds: true}
		wake(t, app, harnessenv.ChatGPTApp, "/usr/bin/true")
		if len(app.opened) != 0 {
			t.Error("opened a thread the app already had loaded: that pulls the app to the " +
				"front on every message for nothing the queue was not already doing")
		}
	})
	t.Run("an agent that does not run in the app is never opened in it", func(t *testing.T) {
		for _, surface := range []string{harnessenv.CodexOutsideApp, "", "cli"} {
			app := &fakeApp{}
			wake(t, app, surface, "/usr/bin/true")
			if len(app.opened) != 0 {
				t.Errorf("surface %q: opened the agent's thread in the ChatGPT app, which is "+
					"moving it to an environment it did not run in", surface)
			}
		}
	})
	t.Run("the queue failed: nothing is opened", func(t *testing.T) {
		app := &fakeApp{}
		if wake(t, app, harnessenv.ChatGPTApp, "/usr/bin/false") {
			t.Fatal("setup: a failing queue command was reported as a wake")
		}
		if len(app.opened) != 0 {
			t.Error("opened the thread although nothing was queued for it: the agent " +
				"wakes in front of the operator with no message to act on")
		}
	})
}

// The thread id is the agent's to state and becomes part of a URL, so an id
// that is not one is refused rather than escaped.
func TestAThreadIDThatIsNotOneNeverReachesTheApp(t *testing.T) {
	for _, bad := range []string{"", "x/../y", "a?b=c", "id with space", "a#b"} {
		if argv := harnessenv.OpenArgv(harnessenv.ChatGPTApp, bad); argv != nil {
			t.Errorf("thread %q produced %q", bad, argv)
		}
	}
}

// For an agent on another machine the app is on THAT machine, so the hub hands
// its bridge the surface along with the thread, and the bridge opens it there.
// Asserted on what the bridge actually receives, through the planner and the
// request channel production uses: a bridge test that builds the request by
// hand would pass with the hub never sending the field.
func TestARemoteWakeTellsTheBridgeWhichAppTheAgentRunsIn(t *testing.T) {
	e := hubEngine()
	e.SetWakeCommands(map[string]WakeCommand{"codex": {Argv: []string{"codex", "queue", "{thread}"}}})
	reqs, release, err := e.AttachHostBridge("laptop", []string{"Codex"})
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	far := remoteAgent("far", "laptop")
	far.Agent.Surface = harnessenv.ChatGPTApp
	plan, ok := e.wakeFor(far, core.MsgQuestion, questionFor("far"))
	if !ok || plan.host != "laptop" {
		t.Fatalf("setup: no remote wake planned: ok=%v plan=%+v", ok, plan)
	}
	done := make(chan bool, 1)
	go func() { done <- e.requestRemoteWakeWithin(plan, "far", 5*time.Second) }()
	var got WakeRequest
	select {
	case got = <-reqs:
	case <-time.After(5 * time.Second):
		t.Fatal("setup: the bridge never received the request")
	}
	e.ReportWakeResult(WakeResult{ID: got.ID, Host: "laptop", OK: true})
	<-done
	if got.Surface != harnessenv.ChatGPTApp {
		t.Errorf("the bridge was told the agent runs in %q: it queues the message and never "+
			"opens the thread in the app on its machine", got.Surface)
	}
}

// An agent whose bridge has not said where it runs is opened in the app its
// thread was born in. This is the case after any install: every dormant app
// agent is still on its old bridge, and would otherwise read as unknown at
// exactly the wake that has to open the app. A bridge that DID say a terminal
// keeps it out of the app even though the thread began there.
func TestADormantAppThreadIsOpenedWhereItWasBorn(t *testing.T) {
	if _, err := os.Stat("/usr/bin/true"); err != nil {
		t.Skip("no /usr/bin/true on this platform")
	}
	const thread = "01a0f45e-cbf8-7ef0-acb8-79c25cb4343d"
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	t.Setenv("DIBS_TEST_CODEX_HOME_SET", "1")
	at := time.UnixMilli(0x01a0f45ecbf8)
	dir := filepath.Join(home, "sessions", at.Format("2006"), at.Format("01"), at.Format("02"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	head := `{"type":"session_meta","payload":{"id":"` + thread + `","originator":"Codex Desktop"}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "rollout-x-"+thread+".jsonl"), []byte(head), 0o600); err != nil {
		t.Fatal(err)
	}
	wake := func(surface string) *fakeApp {
		t.Helper()
		app := &fakeApp{}
		app.install(t)
		e := New(core.NewState("test", core.DefaultLimits()), &memLedger{}, deadProber{})
		e.SetWakeCommands(map[string]WakeCommand{"codex": {Argv: []string{"/usr/bin/true", "{thread}"}}})
		l := &core.Agent{
			ID: "worker", Name: "worker", Status: core.StatusDormant, SessionID: thread,
			Agent: &core.AgentInfo{Harness: "Codex", CWD: t.TempDir(), Surface: surface},
			Slots: map[string]core.Slot{},
		}
		e.state.Agents["worker"] = l
		plan, ok := e.wakeFor(l, core.MsgQuestion, questionFor("worker"))
		if !ok || len(plan.argv) == 0 {
			t.Fatal("setup: no command plan")
		}
		e.runWake(plan, "worker")
		return app
	}
	if app := wake(""); len(app.opened) != 1 {
		t.Errorf("a dormant agent whose thread the app created was not opened in the app (%q): "+
			"its message waits in a thread nobody loaded", app.opened)
	}
	if app := wake(harnessenv.CodexOutsideApp); len(app.opened) != 0 {
		t.Errorf("an agent whose bridge said it now runs in a terminal was pulled back into the app: %q", app.opened)
	}
}

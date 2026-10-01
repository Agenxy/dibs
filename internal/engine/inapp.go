package engine

import (
	"log/slog"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/harnessenv"
)

// Waking an agent IN the app it runs in.
//
// An agent belongs to the environment it started in and is woken there and
// nowhere else. For a ChatGPT-app agent, the operator's `codex queue` puts the
// message in the thread's queue, and the app delivers it when it has the thread
// loaded: at once if it already does, and only when somebody opens the thread
// if it does not, which after an app update was every thread. So when the app
// is not holding the thread, Dibs asks the app to open it through the app's own
// codex:// route, which loads it in the app's runtime and launches the app
// first if it is closed. The agent then acts in the app the operator is
// looking at.
//
// Never anywhere else. Opening a thread in an app the agent did not run in is a
// relocation, not a wake, so this happens only for an agent whose bridge
// DERIVED the app from its process tree, or, when no bridge has said, whose
// thread was born in the app (see harnessenv.AppFor). A Codex agent in a
// terminal is never opened in the app.

// shower is the contact with the real app, replaced by tests.
var shower = harnessenv.RealShower

// showInApp runs after the message was queued, off the writer loop like every
// wake. A failure here is logged and does not fail the wake: the message is in
// the thread's queue, and the app delivers it whenever the thread is opened.
//
// Which app is decided HERE, off the writer loop, because when no bridge has
// stated one it is read from the thread's transcript on disk (harnessenv.AppFor).
func (e *Engine) showInApp(plan wakePlan, agent string) {
	if plan.thread == "" {
		return
	}
	argv := harnessenv.OpenArgv(harnessenv.AppFor(plan.surface, plan.harness, plan.thread), plan.thread)
	opened, err := shower.Show(argv, plan.thread)
	switch {
	case err != nil:
		slog.Warn("could not open the agent's thread in its app; the message waits there "+
			"until the thread is opened", "agent", agent, "err", err)
	case opened:
		slog.Info("opened the agent's thread in the app it runs in, so the message is delivered there",
			"agent", agent)
	}
}

func surfaceOf(l *core.Agent) string {
	if l == nil || l.Agent == nil {
		return ""
	}
	return l.Agent.Surface
}

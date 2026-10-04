package engine

import (
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/harnessenv"
)

// Send returns before its background command exits, so a configured queue is
// not an acceptance receipt. Describe acceptance only after it was observed.
// Either note replaces the fold's misleading "when it next wakes" diagnosis.
func (e *Engine) appQueueNote(agent *core.Agent) string {
	if surfaceOf(agent) != harnessenv.ChatGPTApp {
		return ""
	}
	e.wakers.mu.Lock()
	defer e.wakers.mu.Unlock()
	cmd := e.wakers.byHarness[wakeHarness(agent)]
	if !isQueuingArgv(cmd.argv) {
		return ""
	}
	if e.wakeStillQueuedLocked(agent, time.Now()) {
		return "delivered to " + agent.ID + ". Its wake notice is queued in the app; " +
			"it runs when the thread is open, or when Dibs opens it while the person is away."
	}
	return "delivered to " + agent.ID + ". Dibs delivers wake notices through its app queue; " +
		"they run when the thread is open, or when Dibs opens it while the person is away. " +
		"Queue acceptance has not been confirmed for this delivery."
}

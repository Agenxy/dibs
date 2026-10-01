package engine

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// Telling the agent that is waiting that the board could not wake the one it
// is waiting on.
//
// A FAILED WAKE WAS A LOG LINE, AND ONLY A LOG LINE. k7-dev sent two requests to
// two Codex workers; every wake failed, because Codex refused to run in a
// directory that was not a git repository. The daemon knew precisely what had
// happened and wrote it at WARN into dibd.log, which no agent can read. The
// sender's await_events showed no wake and no failure, both requests sat
// pending, doctor went on claiming "a wake either runs or reports why", and
// after the third failure the board quietly stopped trying. It reported why to
// nobody who was waiting.
//
// the maintainer's rule: an agent that is not archived is live and is woken when needed,
// and when the board cannot wake it, whoever is waiting on it is told, rather
// than left to read silence as progress. The channel already exists for exactly
// this, "something happened that you could not have inferred": a notice, which
// reaches the sender through its own digest and wakes it if the operator asked
// notices to.
//
// WHAT THE NOTICE LEAVES OUT: the recipient's wake command. It names the
// recipient's thread, and fixing it belongs to whoever runs the board, who has
// the exact command in dibd.log and on the row. The sender needs to know its
// message has not been seen and that it is still owed; nothing more is theirs.

// wakeFailNotice records, per waiting message, how much its sender has been
// told, so a retrying wake does not repeat itself: once when the wake first
// fails, and once more if the board gives up, which is a different fact.
const (
	toldFailing = 1
	toldStopped = 2
)

// reportWakeFailure tells the agents waiting on this one that the board could
// not wake it. Called from the goroutine that ran the wake, which holds no
// lock, so it can post to the loop.
func (e *Engine) reportWakeFailure(agent string) {
	_, _ = e.query(context.Background(), func() core.Result {
		e.reportWakeFailureDecision(agent, time.Now())
		return core.Result{}
	})
}

// reportWakeFailureDecision is the report, on the loop, split so a test can
// reach it without a process.
func (e *Engine) reportWakeFailureDecision(agent string, now time.Time) {
	e.wakers.mu.Lock()
	gaveUp := e.spawnGivenUp(agent)
	fails := e.wakers.fails[agent]
	e.wakers.mu.Unlock()
	for _, m := range e.state.Inbox(agent) {
		if m.From == "" || m.From == agent || !m.Expecting() ||
			(m.State != core.MsgStatePending && m.State != core.MsgStateDelivered) {
			continue
		}
		want, text := toldFailing, fmt.Sprintf("the board tried to wake %q for your %s #%d and "+
			"the wake failed, so it has not seen it yet. It is still in %s's inbox and is "+
			"read at its next activation; the board will try again", agent, m.Type, m.Serial, agent)
		if gaveUp {
			want, text = toldStopped, fmt.Sprintf("the board has stopped trying to wake %q: "+
				"its wake failed %d times in a row. Your %s #%d waits in its inbox until it is "+
				"next active, or until whoever runs this board fixes how it is woken",
				agent, fails, m.Type, m.Serial)
		}
		key := agent + "\x00" + strconv.FormatUint(m.Serial, 10)
		e.wakers.mu.Lock()
		already := e.wakers.failTold[key]
		if already < want {
			if e.wakers.failTold == nil {
				e.wakers.failTold = map[string]int{}
			}
			e.wakers.failTold[key] = want
		}
		e.wakers.mu.Unlock()
		if already >= want {
			continue
		}
		e.pushNoticeFor(m.From, text, e.state.Serial, m.Serial, now)
	}
}

// forgetWakeFailures clears what senders were told about this agent, when a
// wake for it finally works: the next failure is news again. Caller holds no
// lock.
func (e *Engine) forgetWakeFailures(agent string) {
	e.wakers.mu.Lock()
	defer e.wakers.mu.Unlock()
	prefix := agent + "\x00"
	for k := range e.wakers.failTold {
		if len(k) > len(prefix) && k[:len(prefix)] == prefix {
			delete(e.wakers.failTold, k)
		}
	}
}

// wakeStatusOf is what the board row says about reaching this agent: "" when
// nothing is wrong. The row is where the person running the board looks, and
// until this it showed a worker as active while every wake for it was failing.
func (e *Engine) wakeStatusOf(agent string) string {
	e.wakers.mu.Lock()
	defer e.wakers.mu.Unlock()
	switch n := e.wakers.fails[agent]; {
	case n >= spawnFailureCap:
		return "stopped: " + strconv.Itoa(n) + " wakes failed in a row; see dibd.log for the command"
	case n > 0:
		return "failing: the last wake did not run; see dibd.log for the command"
	}
	return ""
}

// runWakeAndReport runs one wake and settles what its outcome means for the
// agents waiting: on success their failure history is cleared, on failure they
// are told. Returns whether the wake worked.
//
// ONE PLACE, and that is the point of it existing. Both wake goroutines, the
// first attempt and the retry, used to carry their own copy of the success
// bookkeeping, and the first version of this change added the failure report
// to each copy. A test drives one of them. A copy that only the untested
// goroutine reaches is exactly how #245 shipped a feature called from nowhere,
// so there is no copy: both call this.
func (e *Engine) runWakeAndReport(cmd wakePlan, agent string) bool {
	if e.runWake(cmd, agent) {
		e.clearWakeAttempts(agent)
		e.forgetWakeFailures(agent)
		return true
	}
	e.reportWakeFailure(agent)
	return false
}

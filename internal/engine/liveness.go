package engine

import (
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// Deciding whether an agent is there, without asking it.
//
// THE DAEMON HELD THE PROOF AND COMPLAINED ANYWAY, which is the defect this
// file exists to end. A harness lifecycle hook firing means that session exists
// and has just taken a turn. The daemon recorded those hooks, judged staleness
// on a different clock, swept the row dormant, and then delivered ON THAT SAME
// HOOK a line reading "you have not coordinated with the board for 9h33m:
// your declaration reads stale and peers writing to you may be told you are
// dormant. check_in now". The operator read it off their own screen and asked
// why an agent has to keep announcing something the board can see.
//
// THREE CLOCKS, READ IN DIFFERENT COMBINATIONS BY DIFFERENT CALLERS, which is
// how one daemon held three views of one fact:
//
//   - Agent.LastCoordination is durable and deliberately coarse. It is
//     checkpointed once per AgentTTL/2, so a perfectly healthy agent's ledgered
//     timestamp is routinely minutes old. Alone, it means "when did this agent
//     last write to the ledger", which is not "is anybody there".
//   - Engine.seen is ephemeral and answers a DIFFERENT question: would a wake
//     collide with a turn already running. A finishing hook must not stamp it,
//     because a Stop means the turn is over and a wake is now exactly what
//     should happen.
//   - Engine.hookAlive is this file's addition: a hook fired, so the session is
//     there. Never consulted by the wake path, so it cannot suppress a wake.
//
// The board row read two of them, the sweep read two, the retired reminder read
// one, and they disagreed in public: a fresh last_seen beside a dormant status
// beside a complaint about hours of silence, each true of a different clock.
// lastEvidenceOf is the single answer all three now use.
//
// WHAT IS NOT DERIVABLE, and is therefore still the agent's own business: what
// the agent is DOING. A declaration can be hours out of date while the agent is
// demonstrably alive, and no hook can tell the difference. Dibs does not nag
// about that, because an agent whose work moves on declares it, and a reminder
// that fires on a timer rather than on a fact is the thing that was trained
// away as noise.

// lastEvidenceOf is the most recent moment this daemon has any evidence the
// agent was there: a ledgered op, an authenticated read, or a lifecycle hook.
//
// Must run inside the loop: it reads the ephemeral maps.
func (e *Engine) lastEvidenceOf(l *core.Agent) time.Time {
	if l == nil {
		return time.Time{}
	}
	at := l.LastCoordination
	if t, ok := e.seen[l.ID]; ok && t.After(at) {
		at = t
	}
	if t, ok := e.hookAlive[l.ID]; ok && t.After(at) {
		at = t
	}
	return at
}

// noteHookAlive records that a harness hook fired for this agent.
//
// Called for EVERY hook, not only the recognised lifecycle states. An
// unrecognised event is still a hook, still arrived from a live session, and
// still proves the thing being measured; keying on the states would leave a
// thin path on which the daemon holds evidence it declines to use, which is the
// shape of the original defect.
func (e *Engine) noteHookAlive(l *core.Agent) {
	if l == nil {
		return
	}
	if e.hookAlive == nil {
		e.hookAlive = map[string]time.Time{}
	}
	e.hookAlive[l.ID] = time.Now()
}

// sessionMovedProcess reports whether this agent's recorded process is gone
// but its harness session is alive in ANOTHER process: the session moved, the
// agent did not die.
//
// A DEAD PID WAS BEATING DIRECT EVIDENCE, and it locked an agent out. A pid is
// recorded once, at register, and a harness can restart the process behind a
// session without the agent registering again: switching the model in Claude
// Code does exactly that, resuming the same session id in a new process. The
// sweep then probed the old pid, found it gone, and marked the agent
// process_exited. Every call the agent made woke it again, which re-armed the
// awareness gate; check_in acknowledged the board; and the next sweep killed it
// again before the agent's next call. So declare was refused on every attempt,
// and its hint said to call check_in, which the agent had just done. An agent
// following the hints looped forever, on a board that showed it as crashed
// while it was talking to the board. Found by the architect, locked out of
// updating its own declaration.
//
// The harness already publishes the answer. Each Claude Code session writes a
// sidecar naming its live process, and the peer snapshot holds only sessions
// whose process is alive, so a bound session present in it is running, whatever
// the recorded pid says. A real crash takes the session's process with it and
// drops it from the snapshot, so crash detection is kept: it waits at most one
// snapshot refresh.
//
// Not a claim that the recorded pid is alive, which is why the sweep records
// neither AlivePIDs nor DeadAgents for this case: the ledger's proc_alive is a
// measurement of l.PID, and this agent is not at l.PID any more. It is judged
// by lastEvidenceOf like an agent with no pid at all, which is the honest
// position for an agent whose pid is known to be stale.
//
// Harnesses that publish no sidecar are unchanged: the snapshot has nothing
// for them, and a dead pid still means dead.
func (e *Engine) sessionMovedProcess(l *core.Agent) bool {
	if l == nil {
		return false
	}
	s, ok := peerSessionIn(e.peerSnapshot(), sessionsOf(l))
	return ok && s.PID != 0 && s.PID != l.PID
}

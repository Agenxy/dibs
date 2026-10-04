package engine

import (
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// Continuing a turn that ended with declared work still open.
//
// THE STALL this exists for, measured on 2026-09-30 in a Codex worker's own
// transcript. Mail woke it through the ChatGPT app; the turn's only prompt was
// "Dibs: a new question is waiting."; the model took answering that as the
// whole task, declared "Implementing ... C" a few seconds before the end, wrote
// "No other messages are pending" and completed. Nothing was going to start it
// again: the work it had just declared sat undone for hours until a person
// noticed. Wake-on-mail worked. Continuation was missing.
//
// A declaration is the agent's own statement that it is working on something.
// A turn that ends while one is open, and not marked waiting, ended with work
// in progress, so at Stop Dibs answers the way both harnesses continue a turn
// (`decision: "block"`, with a reason that becomes the next prompt) and names
// the declaration in the agent's own words. Not a peer's body and not a
// decision about what to do: the agent's commitment, handed back with the two
// calls that settle it if it is no longer true. AGENTS.md rule 5 states where
// the line now is.
//
// ONLY A TURN DIBS STARTED. An interactive session ends its turn to hand back
// to a person, and continuing it because a declaration is open would talk over
// them. So this applies after a wake Dibs delivered, and stops the moment a
// person prompts (UserPromptSubmit, which Claude Code reports and the Codex
// plugin does not bind, so for Codex the guard is the wake alone).
//
// BOUNDED. At most maxContinuations for one version of the declarations, and
// the count resets on progress the daemon can see for itself: the declaration
// changing (a new ref such as a PR, new text), or a turn after the last
// continuation that ran progressTurn or longer. An agent that stops twice
// without either is left alone, and a later phase reports it as stalled.

const (
	maxContinuations = 2
	progressTurn     = 10 * time.Minute
	// maxInWindow continuations in continuationWindow, whatever the
	// declaration does. A changed declaration is progress, and that let a
	// loop through: measured 2026-10-01, a Codex worker whose session's tool
	// list predated `waiting` rewrote its declaration's TEXT to say it was
	// waiting, each rewrite reset the per-version budget, and its Stop was
	// continued four times in three minutes before it found the field. The
	// per-version budget asks "did it move on"; this asks "is this a loop".
	maxInWindow        = 3
	continuationWindow = 15 * time.Minute
	// maxQuoted bounds how much of a declaration is quoted back.
	maxQuoted = 600
)

// continuation is one agent's record: what it was last continued for.
type continuation struct {
	count   int
	version uint64 // newest UpdatedSerial among its open declarations
	at      time.Time
	recent  []time.Time // every continuation inside continuationWindow
}

// noteDibsStartedTurn records that a wake Dibs delivered started this agent's
// next turn, which is what makes continuing it legitimate.
func (e *Engine) noteDibsStartedTurn(agent string, now time.Time) {
	e.wakers.mu.Lock()
	defer e.wakers.mu.Unlock()
	if e.wakers.dibsTurn == nil {
		e.wakers.dibsTurn = map[string]time.Time{}
	}
	e.wakers.dibsTurn[agent] = now
}

// notePersonPrompted forgets that: a person is in the conversation now.
func (e *Engine) notePersonPrompted(agent string) {
	e.wakers.mu.Lock()
	defer e.wakers.mu.Unlock()
	delete(e.wakers.dibsTurn, agent)
}

// openDeclarations are the agent's declarations that say it is WORKING:
// declared and not waiting. Sorted by slot id so the reason reads the same
// every time.
func openDeclarations(l *core.Agent) []core.Slot { return openOf(slotsOf(l)) }

// openOf is openDeclarations over any set of commitments, obligations
// included (obligations.go).
func openOf(slots []core.Slot) []core.Slot {
	var open []core.Slot
	for _, s := range slots {
		if strings.TrimSpace(s.Waiting) == "" && strings.TrimSpace(s.Text) != "" {
			open = append(open, s)
		}
	}
	sort.Slice(open, func(i, j int) bool { return open[i].ID < open[j].ID })
	return open
}

// decideContinuation is the decision, apart from the engine so it can be
// tested without one: whether to continue, the reason to give, and the record
// to keep.
func decideContinuation(
	open []core.Slot, dibsStarted bool, prev continuation, now time.Time,
) (string, continuation, bool) {
	if !dibsStarted || len(open) == 0 {
		return "", prev, false
	}
	var version uint64
	for _, s := range open {
		version = max(version, s.UpdatedSerial)
	}
	next := prev
	if prev.version != version || now.Sub(prev.at) >= progressTurn {
		next.count = 0 // progress: the declaration moved, or the last turn ran long
	}
	if next.count >= maxContinuations {
		return "", next, false
	}
	var recent []time.Time
	for _, t := range prev.recent {
		if now.Sub(t) < continuationWindow {
			recent = append(recent, t)
		}
	}
	next.recent = recent
	if len(recent) >= maxInWindow {
		return "", next, false // a loop, whatever the declaration says: see maxInWindow
	}
	recent = append(recent, now)
	next.count++
	next.version, next.at, next.recent = version, now, recent
	return continuationReason(open, next.count), next, true
}

// continuationReason is what the model reads as its next prompt: its own
// declarations, verbatim, and the calls that settle each case.
func continuationReason(open []core.Slot, n int) string {
	var b strings.Builder
	b.WriteString("Dibs: this turn is ending, and you still hold ")
	if len(open) == 1 {
		b.WriteString("a declaration that says you are working:")
	} else {
		fmt.Fprintf(&b, "%d declarations that say you are working:", len(open))
	}
	for _, s := range open {
		text := s.Text
		if len(text) > maxQuoted {
			text = text[:maxQuoted] + "..."
		}
		fmt.Fprintf(&b, "\n  %s: %q", s.ID, text)
	}
	// THE FIELD, NAMED AS A FIELD. A long-lived session keeps the tool list
	// it started with, so a worker can be older than `waiting`; one put
	// "waiting" in its declaration's text, which marks nothing, and was
	// continued again. Saying it is an argument of declare reaches a model
	// whose schema does not list it.
	b.WriteString("\nIf that work is not finished, this turn is yours to continue it. " +
		"If it is finished, undeclare it. If it is blocked, call declare with the same " +
		"slot_id and the `waiting` argument set (on whom or what, e.g. \"ci\"), plus " +
		"`recheck_after` (e.g. \"20m\") if nothing will tell you; writing \"waiting\" in " +
		"the text marks nothing. ")
	for _, s := range open {
		if serial, ok := strings.CutPrefix(s.ID, "request "); ok {
			// An owed request has no slot of its own to mark waiting; this is
			// how a worker parks one (obligations.go, parkedBy).
			fmt.Fprintf(&b, "An owed request that is blocked on someone else is parked by a "+
				"waiting declaration whose refs include \"request:%s\"; report it done "+
				"only when it is. ", serial)
			break
		}
	}
	fmt.Fprintf(&b, "(Continuation %d of %d until the declaration changes.)", n, maxContinuations)
	return b.String()
}

// continueDeclaredWork applies the decision for one agent at Stop. Called on
// the writer loop, from HookPollFrom.
func (e *Engine) continueDeclaredWork(l *core.Agent, now time.Time) (string, bool) {
	e.wakers.mu.Lock()
	defer e.wakers.mu.Unlock()
	_, started := e.wakers.dibsTurn[l.ID]
	if e.wakers.continued == nil {
		e.wakers.continued = map[string]continuation{}
	}
	reason, next, ok := decideContinuation(openOf(e.workSlotsOf(l, now)), started, e.wakers.continued[l.ID], now)
	e.wakers.continued[l.ID] = next
	if ok {
		// Said in the log, because the only other trace is in the agent's
		// own transcript, which is where the first live check had to look.
		slog.Info("continued a turn that ended with declared work open",
			"agent", l.ID, "continuation", next.count, "in_window", len(next.recent))
	}
	return reason, ok
}

// stopContinuation is continueDeclaredWork for the hook path: only at the
// event that ends a turn, and never on a turn a Stop hook already continued
// (Codex reports that as stop_hook_active; the budget covers Claude Code,
// whose field means something else).
func (e *Engine) stopContinuation(l *core.Agent, event string, stopActive bool, now time.Time) (string, bool) {
	if !isStopEvent(event) || event == "SubagentStop" || stopActive {
		return "", false
	}
	return e.continueDeclaredWork(l, now)
}

// continuationReply is the Stop reply that continues the turn, or nil.
func (e *Engine) continuationReply(l *core.Agent, event string, stopActive bool) core.Result {
	reason, ok := e.stopContinuation(l, event, stopActive, time.Now())
	if !ok {
		return nil
	}
	// A continued Stop is not a turn that ended: the turn goes on. Recorded
	// as ended, the later wakes in stall.go would count from a stop that did
	// not happen. On the writer loop, which owns turnEnded.
	delete(e.turnEnded, l.ID)
	e.noteSocketBusy(l, time.Now())
	return core.Result{"decision": "block", "reason": reason}
}

// notePromptFrom records a person's prompt: its turns are theirs, and Dibs
// does not continue them.
func (e *Engine) notePromptFrom(agent, event string) {
	if event == "UserPromptSubmit" {
		e.notePersonPrompted(agent)
	}
}

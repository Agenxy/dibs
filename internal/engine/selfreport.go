package engine

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// Dibs reporting its own faults, to the agent whose job is to fix them.
//
// A coordination service that notices something wrong with itself and writes it
// to a log file has told nobody. The operator is not tailing it, and the whole
// premise of this product is that a human should not have to be the one who
// notices. Meanwhile a fleet usually has an agent whose standing role IS
// administering this board, and that agent reads its mail.
//
// So faults go to the coordinator as ordinary mail, and to the human when
// nobody holds the role. Ordinary, deliberately: the same envelope, the same
// mailbox, the same wake path, visible on the same board, and replayable. A
// private channel for system messages would be a second delivery mechanism to
// keep working, and the first thing to rot.
//
// WHAT A REPORT SAYS is the part that matters. "Something failed" is a fault
// report; it is not a useful one. Every report names what happened, and then
// what to do about it, and those are two different sentences depending on
// whether this is the operator's configuration or Dibs' own defect:
//
//   - Configuration: the remedy, precisely, because it is theirs to apply.
//   - A defect: the repository, and an invitation to FIX it. An agent that has
//     just been handed a reproducible fault, in a Go codebase, with the failing
//     path named, is in a better position to write that patch than anyone who
//     will read the issue later. Asking for a bug report gets a bug report;
//     asking for a patch sometimes gets a patch, and a contributor.
//
// Nothing here drives anybody. It is a message; an agent may act on it, file
// it, or ignore it, exactly as with any other message on this board.

// Fault is one thing Dibs noticed about itself.
type Fault struct {
	// Kind dedupes. One report per kind per daemon run: a fault that recurs
	// every thirty seconds must not become thirty messages, and a fault that
	// survives a restart is worth saying again because somebody may have tried
	// to fix it.
	Kind string
	// What happened, in the operator's terms rather than the stack's.
	What string
	// Remedy is what to do. Required: a report without one is an alarm.
	Remedy string
	// Bug marks a fault that is Dibs' fault rather than the machine's. It
	// changes the ask from "fix your setup" to "fix ours, and send it back".
	Bug bool
	// Where names the code path, when known. Only useful on a Bug, and then it
	// is the difference between a patch and a bug report.
	Where string
}

// repoURL is where a fix goes. One place, because it appears in prose that gets
// read by somebody deciding whether contributing is worth the friction.
const repoURL = "https://github.com/agenxy/dibs"

type faultState struct {
	// mu guards seen, flushing and pending: the bookkeeping the writer loop
	// touches on every tick through flushFaults.
	mu   sync.Mutex
	seen map[string]bool

	// idMu guards nothing but the identity mint below, and is separate from mu
	// for a reason worth stating.
	//
	// dibsAgent holds a lock across e.Do, which waits for the writer loop. When
	// that lock was mu, the loop's own tick entered flushFaults and blocked
	// acquiring it: the reporting goroutine waited for the writer, the writer
	// waited for the reporting goroutine's mutex, and the only receiver of
	// e.ops was gone. A bounded context makes that a multi-second freeze of the
	// whole board; an unbounded one makes it permanent.
	//
	// I introduced the cycle by putting flushFaults on the loop. Two mutexes,
	// because the two things genuinely are separate: one is a cached identity,
	// the other is what has been reported. Found by a pre-release review, and it
	// is the third instance of this shape from me in as many days: anything the
	// loop calls must not wait for anything the loop is needed to finish.
	idMu sync.Mutex
	// flushing guards the off-loop delivery below, so a sweep every second does
	// not pile up goroutines all trying to deliver the same held faults.
	flushing bool
	// pending holds faults found while the board had nobody to tell.
	//
	// The startup reachability check is the case that matters: it runs before
	// any agent has registered, so there is no coordinator and no human, and
	// the fault was logged and then never revisited. The coordinator who joins
	// a minute later heard nothing, and nothing scheduled a retry. The flag is
	// already set on DELIVERY rather than on the attempt, so all that was
	// missing was somebody trying again. Found by a pre-release review.
	pending []Fault
}

// ReportFault tells the coordinator, or the human, that something is wrong.
//
// Best effort by construction. This runs because something already failed, so
// it must not be able to fail loudly on top: every path out of here logs and
// returns, and the caller is never asked to handle an error from the reporter.
func (e *Engine) ReportFault(ctx context.Context, f Fault) {
	if f.Kind == "" || f.What == "" || f.Remedy == "" {
		// A report with no remedy is an alarm, and this file exists to not
		// produce those. Caught here rather than in review.
		slog.Warn("incomplete fault report suppressed", "kind", f.Kind)
		return
	}
	// Marked seen when it is DELIVERED, never before.
	//
	// This set the flag first and returned early on four paths after it: no
	// coordinator and no human on the board, the reporting identity failing to
	// mint, the only recipient being itself, and the send erroring. Every one of
	// those suppressed the fault permanently for the run while nobody had been
	// told, which is the "a flag stood in for the work" bug this file was
	// written to stop producing. The startup reachability check is exactly when
	// it bites: it runs before any agent has registered, so the coordinator who
	// joins a minute later never hears about it.
	//
	// Found by an independent review before release.
	e.faults.mu.Lock()
	already := e.faults.seen[f.Kind]
	e.faults.mu.Unlock()
	if already {
		return
	}

	to := e.coordinatorOrHuman()
	if to == "" {
		// Nobody to tell YET. Said out loud, because "reported" and "there was
		// nobody on the board" must not look the same from outside, and held so
		// that whoever arrives next hears it. See faultState.pending.
		slog.Warn("dibs found a fault and has nobody to report it to",
			"kind", f.Kind, "what", f.What, "remedy", f.Remedy)
		e.holdFault(f)
		return
	}

	from, token, err := e.dibsAgent(ctx)
	if err != nil {
		slog.Warn("could not report a fault", "kind", f.Kind, "err", err)
		return
	}
	if from == to {
		return // never report to itself
	}
	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpSendMessage, Token: token, To: to,
		// notify, never request: nothing here is waiting on an answer, and a
		// type that wakes somebody is for a peer who is blocked. A fault report
		// that interrupts is a fault report people turn off.
		MsgType: core.MsgNotify,
		Body:    faultBody(f),
	}); err != nil {
		slog.Warn("could not report a fault", "kind", f.Kind, "err", err)
		return // not delivered, so not seen: the next attempt may find a reader
	}
	e.faults.mu.Lock()
	if e.faults.seen == nil {
		e.faults.seen = map[string]bool{}
	}
	e.faults.seen[f.Kind] = true
	e.faults.mu.Unlock()
}

// faultBody writes the message. Kept separate from delivering it so the wording
// can be tested without a board, a coordinator, or a failure.
func faultBody(f Fault) string {
	var b strings.Builder
	b.WriteString(f.What)
	b.WriteString("\n\n")
	if f.Bug {
		b.WriteString("This looks like a defect in Dibs rather than anything you " +
			"configured.\n\n")
		if f.Where != "" {
			b.WriteString("Where: " + f.Where + "\n")
		}
		b.WriteString(f.Remedy)
		b.WriteString("\n\nIf you can see the fix, send it: " + repoURL +
			". You are holding a reproducible fault in a Go codebase with the failing " +
			"path named, which is a better starting point than whoever reads the issue " +
			"later will have. A patch is more welcome than a report, and a report is " +
			"very welcome.")
		return b.String()
	}
	b.WriteString("This is configuration on this machine, not a defect.\n\n")
	b.WriteString(f.Remedy)
	b.WriteString("\n\nIf you follow that and it still happens, it is ours: " + repoURL)
	return b.String()
}

// dibsName is the participant Dibs speaks as.
//
// It gets an AGENT identity for the same reason the human does: so its messages
// are ordinary messages, delivered down the paths that already work, visible on
// the board, and replayable. Nothing in internal/core learns that a daemon can
// send mail.
const dibsName = "dibs"

func dibsNonce() string { return "system:dibs" }

// dibsAgent returns the identity Dibs sends as, creating it on first use.
//
// On first USE, not at startup: a board where nothing has ever gone wrong should
// not carry a row for the thing that reports what goes wrong. Same rule as the
// human's identity, and the same reason.
func (e *Engine) dibsAgent(ctx context.Context) (agent, token string, err error) {
	// idMu, never mu: this holds a lock across e.Do. See faultState.idMu.
	e.faults.idMu.Lock()
	defer e.faults.idMu.Unlock()
	res, err := e.Do(ctx, &core.Op{
		// The one registration allowed to be this identity, like the human's.
		HumanMint: true,
		Kind:      core.OpRegister, Name: dibsName,
		Description: "Dibs itself, reporting faults it found in its own operation",
		AgentKind:   core.KindPersistent,
		Nonce:       dibsNonce(),
		SessionID:   dibsNonce(),
		// A daemon has a process, but this identity is not it: reporting a fault
		// must not make the reporter look like a crashed agent when the daemon
		// restarts, which is the exact bug the human's row had.
		NoProcess: true,
		Agent: &core.AgentInfo{
			Harness: "dibs", Surface: "daemon", Host: hostname(),
		},
	})
	if err != nil {
		return "", "", err
	}
	tok, _ := res["token"].(string)
	id, _ := res["agent_id"].(string)
	if tok == "" || id == "" {
		return "", "", core.ErrBadToken
	}
	return id, tok, nil
}

// CheckReachability reports, once, when Dibs cannot get a notification in front
// of the person.
//
// The first customer for fault reporting, and a real one: an operator watched a
// coordinator request post successfully, get swallowed by a Focus mode, and be
// reported as delivered by every layer. Dibs knew. It had nowhere to say so.
func (e *Engine) CheckReachability(ctx context.Context, reaches bool, why string) {
	if reaches || why == "" {
		return
	}
	e.ReportFault(ctx, Fault{
		Kind: "notify-unreachable",
		What: "Dibs cannot get a notification in front of the human: " + why,
		Remedy: "Until this is fixed, a request addressed to them is delivered and " +
			"not seen, and the agent waiting on it will time out. `dibs doctor` " +
			"re-checks this.",
	})
}

// reportNotifyFailure is the fallible half of report(): a notification that
// could not be delivered at all, as opposed to one nobody answered.
func (e *Engine) reportNotifyFailure(err error) {
	if err == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	e.ReportFault(ctx, Fault{
		Kind: "notify-failed",
		What: "A notification to the human could not be delivered: " + err.Error(),
		Remedy: "The message is still on the board and can be read there. " +
			"`dibs doctor` says whether notifications can reach this machine at all.",
	})
}

// coordinatorOrHuman names who a fault report goes to, reading the board
// THROUGH the loop.
//
// e.state's maps belong to the single writer. Reading them from a reporting
// goroutine is a data race and, on a busy board, a concurrent map read and write
// panic: the daemon would crash while telling somebody about a smaller problem.
// -race did not catch it because nothing exercised a report during traffic.
//
// Found by an independent review before release, alongside the same mistake in
// two other new paths.
func (e *Engine) coordinatorOrHuman() string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := e.query(ctx, func() core.Result {
		// A report needs a READER, which is not the same as needing a
		// recipient. CoordinatorID answers "who holds the role", and on a board
		// whose only coordinator is dormant that is an agent which may never
		// come back: the report would be filed correctly into a mailbox nobody
		// opens, which is this file's own failure mode with an extra step.
		//
		// So: a live coordinator first, then the human, then a dormant
		// coordinator as the last resort, because a dormant standing agent may
		// still wake and a person who is not at the keyboard may not.
		live, dormant := e.coordinatorsByLiveness()
		to := live
		if to == "" {
			to = e.humanIdentityLocked()
		}
		if to == "" {
			to = dormant
		}
		return core.Result{"to": to}
	})
	if err != nil {
		return ""
	}
	to, _ := res["to"].(string)
	return to
}

// coordinatorsByLiveness names the best live coordinator and the best dormant
// one, each chosen by id so the answer is stable across calls: map iteration is
// random, and a report that went to a different coordinator each time would be
// a report nobody owns.
//
// Must run inside the loop.
func (e *Engine) coordinatorsByLiveness() (live, dormant string) {
	for id, l := range e.state.Agents {
		if l.Status == core.StatusClosed || l.Status == core.StatusArchived ||
			!l.IsCoordinator() {
			continue
		}
		if l.Status == core.StatusActive {
			if live == "" || id < live {
				live = id
			}
			continue
		}
		if dormant == "" || id < dormant {
			dormant = id
		}
	}
	return live, dormant
}

// holdFault keeps a fault that had no recipient, so it can be delivered when one
// appears.
func (e *Engine) holdFault(f Fault) {
	e.faults.mu.Lock()
	defer e.faults.mu.Unlock()
	for _, p := range e.faults.pending {
		if p.Kind == f.Kind {
			return
		}
	}
	e.faults.pending = append(e.faults.pending, f)
}

// flushFaults delivers anything held from before the board had anybody on it.
//
// Called from the sweep, which is the one thing that runs on its own and
// therefore the only place a report can be retried without an agent's call to
// hang it on.
//
// ON THE LOOP, SO IT DOES ALMOST NOTHING HERE. The first version called
// coordinatorOrHuman from this function, and that runs e.query: the sweep is
// executing ON the writer loop, so it sent to e.ops and waited for the loop it
// was already inside. Not a hang, because the query has a five-second timeout,
// which is worse in its way: every tick stalled for five seconds and then
// carried on as though nothing had happened. It was caught by a panel
// end-to-end check going from green to red on timing alone.
//
// That is the same lock-inversion class as HumanAgent holding its mutex across
// the loop, committed two hours after fixing it, which is a fair measure of how
// easy the shape is to reproduce: anything reachable from the loop must not ask
// the loop for anything.
func (e *Engine) flushFaults() {
	e.faults.mu.Lock()
	pending := len(e.faults.pending) > 0
	busy := e.faults.flushing
	if pending && !busy {
		e.faults.flushing = true
	}
	e.faults.mu.Unlock()
	if !pending || busy {
		return
	}
	go e.deliverHeldFaults()
}

// deliverHeldFaults is the part that talks to the loop, and therefore is not on
// it.
func (e *Engine) deliverHeldFaults() {
	defer func() {
		e.faults.mu.Lock()
		e.faults.flushing = false
		e.faults.mu.Unlock()
	}()

	e.faults.mu.Lock()
	held := e.faults.pending
	e.faults.pending = nil
	e.faults.mu.Unlock()
	if len(held) == 0 {
		return
	}
	if e.coordinatorOrHuman() == "" {
		// Still nobody. Put them back for the next tick rather than dropping
		// them, which is the whole point of holding them at all.
		e.faults.mu.Lock()
		e.faults.pending = append(held, e.faults.pending...)
		e.faults.mu.Unlock()
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, f := range held {
		e.ReportFault(ctx, f)
		// RE-HELD if it did not land. ReportFault marks a kind seen only on
		// delivery, so the absence of that flag is the reliable signal that this
		// one is still owed.
		//
		// The held list was emptied before delivery and never refilled, so a
		// full mailbox, a timeout, or a transient failure to mint the reporting
		// identity lost a one-shot startup reachability report for the life of
		// the daemon. Holding faults exists precisely because the first attempt
		// can fail; dropping them on the second is the same bug one layer in.
		// Found by a pre-release review.
		e.faults.mu.Lock()
		delivered := e.faults.seen[f.Kind]
		if !delivered {
			e.faults.pending = append(e.faults.pending, f)
		}
		e.faults.mu.Unlock()
	}
}

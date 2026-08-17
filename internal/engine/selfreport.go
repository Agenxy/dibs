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
	mu   sync.Mutex
	seen map[string]bool
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
	e.faults.mu.Lock()
	if e.faults.seen == nil {
		e.faults.seen = map[string]bool{}
	}
	already := e.faults.seen[f.Kind]
	e.faults.seen[f.Kind] = true
	e.faults.mu.Unlock()
	if already {
		return
	}

	to := e.state.CoordinatorID()
	if to == "" {
		to = e.HumanIdentity()
	}
	if to == "" {
		// Nobody to tell. Said out loud, because "reported" and "there was
		// nobody on the board" must not look the same from outside.
		slog.Warn("dibs found a fault and has nobody to report it to",
			"kind", f.Kind, "what", f.What, "remedy", f.Remedy)
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
	}
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
	e.faults.mu.Lock()
	defer e.faults.mu.Unlock()
	res, err := e.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: dibsName,
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

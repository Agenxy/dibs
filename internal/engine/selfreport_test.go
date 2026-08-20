package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// A fault report has to end on something the reader can DO.
//
// This whole file exists because a log line is a report to nobody. Replacing it
// with a message to nobody-in-particular would be the same failure with more
// steps, so the wording is the feature: what happened, then what to do, and the
// second one differs by whose fault it is.
func TestAFaultReportSaysWhatToDoAboutIt(t *testing.T) {
	config := faultBody(Fault{
		Kind: "notify-unreachable", What: "Dibs cannot notify you: a Focus mode is on",
		Remedy: "Allow Dibs to break through Focus in System Settings.",
	})
	if !strings.Contains(config, "System Settings") {
		t.Error("a configuration fault did not carry its remedy")
	}
	if strings.Contains(config, "send it:") {
		t.Error("a configuration fault asked the reader to patch Dibs. It is their " +
			"machine that is misconfigured, and telling somebody to fix your code " +
			"when they mis-set a preference wastes the one report they will read")
	}
	// Even so it must not dead-end: if the remedy does not work, the reader
	// needs somewhere to go, and that IS us.
	if !strings.Contains(config, repoURL) {
		t.Error("a configuration fault gave no way to escalate when the remedy fails")
	}

	bug := faultBody(Fault{
		Kind: "ledger", What: "An op would not replay", Bug: true,
		Where:  "internal/core/apply.go",
		Remedy: "The board is intact; the daemon refused the record.",
	})
	if !strings.Contains(bug, repoURL) || !strings.Contains(bug, "internal/core/apply.go") {
		t.Error("a defect report named neither the repository nor the failing path, " +
			"which are the two things that turn a report into a patch")
	}
	// The ask that makes a contributor rather than an issue.
	if !strings.Contains(bug, "send it") {
		t.Error("a defect report did not invite a fix. The agent reading this is " +
			"holding a reproducible fault in a Go codebase with the path named: " +
			"asking for a bug report gets a bug report, asking for a patch " +
			"sometimes gets a patch")
	}
}

// A report with no remedy is an alarm, and this package exists to not produce
// those. Refused at the door rather than delivered as "something went wrong".
func TestAReportWithoutARemedyIsRefused(t *testing.T) {
	e := &Engine{}
	// No panic, no send, nothing on the board: the engine here has no loop, so
	// any attempt to deliver would block forever rather than fail. That is the
	// assertion: this returns.
	done := make(chan struct{})
	go func() {
		defer close(done)
		e.ReportFault(t.Context(), Fault{Kind: "x", What: "something went wrong"})
	}()
	<-done
}

// A fault report needs a READER, not merely a recipient.
//
// The first version asked CoordinatorID, which answers "who holds the role".
// On a board whose only coordinator is dormant, that is an agent which may
// never come back, so the report was filed correctly into a mailbox nobody
// opens: this file's own failure mode, with an extra step. Measured on a real
// board where the standing coordinator had been dormant for a day and the
// operator was at the keyboard the whole time.
func TestAFaultGoesToSomebodyWhoCanReadIt(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	humanID, _, err := e.HumanAgent(ctx)
	if err != nil {
		t.Fatal("setup:", err)
	}
	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: "boss", AgentKind: core.KindPersistent, Nonce: "n-b",
	}); err != nil {
		t.Fatal("setup:", err)
	}
	// The only coordinator is dormant, which is the case that mattered.
	st.Agents["boss"].Role = core.RoleCoordinator
	st.Agents["boss"].Status = core.StatusDormant

	if got := e.coordinatorOrHuman(); got != humanID {
		t.Errorf("the report goes to %q; the only coordinator is dormant and the "+
			"human is right here. A report filed where nobody reads it is the bug "+
			"this file exists to stop producing", got)
	}

	// With a live coordinator, it goes there: faults are the coordinator's job,
	// and this must not become "always tell the human".
	st.Agents["boss"].Status = core.StatusActive
	if got := e.coordinatorOrHuman(); got != "boss" {
		t.Errorf("the report goes to %q with a live coordinator available: "+
			"administering the board is their role, not the operator's", got)
	}
}

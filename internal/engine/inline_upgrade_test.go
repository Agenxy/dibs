package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/ledger"
)

// Opt-in because this fixture is a PRIVATE COPY of an encrypted pre-upgrade
// board. Never point it at a running board: Run records into the copy.
func TestReviewReadUpgradeOnCopiedRealLedger(t *testing.T) {
	dir := os.Getenv("DIBS_TEST_PREUPGRADE_LEDGER_DIR")
	if dir == "" {
		t.Skip("requires copied pre-upgrade ledger and key")
	}
	if !strings.HasPrefix(filepath.Base(dir), "dibs-inline-upgrade.") {
		t.Fatal("fixture must be an explicitly named private upgrade copy, never the live board")
	}
	for _, name := range []string{"key", "ledger.jsonl"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("fixture missing %s", name)
		}
	}
	box, err := ledger.LoadOrCreateKey(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	journal, err := ledger.Open(filepath.Join(dir, "ledger.jsonl"), "upgrade-proof", box)
	if err != nil {
		t.Fatal(err)
	}
	st := core.NewState("upgrade-proof", core.DefaultLimits())
	if _, err := journal.Replay(st); err != nil {
		t.Fatal(err)
	}
	if st.ReviewReadCutoff != 0 {
		t.Fatal("fixture is not pre-upgrade")
	}
	before := st.Serial
	retainedReviews := 0
	for _, m := range st.Messages {
		if (m.State == core.MsgStateApproved || m.State == core.MsgStateDone) && m.RetainUntil.After(time.Now()) {
			for _, p := range m.Progress {
				if p.Review != "" {
					retainedReviews++
				}
			}
		}
	}
	if retainedReviews == 0 {
		t.Fatal("fixture has no retained historical reviews; zero-flood result would prove nothing")
	}
	e := New(st, journal, nil)
	e.SetSocketWakes(false) // no sockets/commands configured or delivered by this fixture
	e.SetWakePolicy(WakeNone)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.Run(ctx); close(done) }()
	_, err = e.query(ctx, func() core.Result {
		if e.state.ReviewReadCutoff != before+1 {
			t.Error("production Run did not record the upgrade cutoff")
		}
		for id := range e.state.Agents {
			if len(e.receivedReviews(id)) != 0 {
				t.Error("historical review would redeliver at upgrade")
			}
		}
		return nil
	})
	cancel()
	<-done
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	journal, err = ledger.Open(filepath.Join(dir, "ledger.jsonl"), "upgrade-proof", box)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := journal.Close(); err != nil {
			t.Error(err)
		}
	}()
	replayedState := core.NewState("upgrade-proof", core.DefaultLimits())
	if _, err := journal.Replay(replayedState); err != nil {
		t.Fatal(err)
	}
	if replayedState.ReviewReadCutoff != before+1 {
		t.Fatal("upgrade cutoff was only in memory")
	}
	t.Logf("pre-upgrade serial=%d; retained historical reviews=%d; review redeliveries=0; cutoff=%d survives encrypted replay", before, retainedReviews, replayedState.ReviewReadCutoff)
}

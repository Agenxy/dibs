package ledger

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/engine"
)

var errHistoryActivitySync = errors.New("fixture sync failure")

type historyActivityFile struct {
	*os.File
	owner     *Ledger
	writes    atomic.Uint64
	syncs     atomic.Uint64
	writeBusy atomic.Bool
	syncBusy  atomic.Bool
	failSync  atomic.Bool
}

func (f *historyActivityFile) Write(p []byte) (int, error) {
	f.writes.Add(1)
	f.writeBusy.Store(historyBusyThroughDoor(f.owner))
	return f.File.Write(p)
}

func (f *historyActivityFile) Sync() error {
	f.syncs.Add(1)
	f.syncBusy.Store(historyBusyThroughDoor(f.owner))
	if f.failSync.Load() {
		return errHistoryActivitySync
	}
	return f.File.Sync()
}

// A private observer interface lets identical behavioral guards run on the old
// source with only its file seam widened. A missing observer reads false: the old proof
// fails AFTER a successful real operation, not at compilation. The test never
// sets the flag or calls a private begin/end helper; real Append must do it.
func historyBusyThroughDoor(l *Ledger) bool {
	observer, ok := any(l).(interface{ historyWriterIsBusy() bool })
	return ok && observer.historyWriterIsBusy()
}

func TestHistoryWriterActivityThroughRealAppend(t *testing.T) {
	dir := t.TempDir()
	box, err := LoadOrCreateKey(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal("setup:", err)
	}
	l, err := Open(filepath.Join(dir, "ledger"), "activity-test", box)
	if err != nil {
		t.Fatal("setup:", err)
	}
	defer func() { _ = l.Close() }()
	file, ok := l.f.(*os.File)
	if !ok {
		t.Fatal("setup: Open did not open a native file")
	}
	f := &historyActivityFile{File: file, owner: l}
	l.f = f
	st := core.NewState("activity-test", core.DefaultLimits())
	if n, err := l.Replay(st); err != nil || n != 0 {
		t.Fatal("setup: actual Replay", n, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	eng := engine.New(st, l, nil)
	joined := make(chan struct{})
	go func() { eng.Run(ctx); close(joined) }()
	defer func() { cancel(); <-joined }()
	result, err := eng.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: "activity", NewToken: "activity", PID: 1,
		Nonce: "history-activity-real-append", AgentKind: core.KindPersistent,
		V7Semantics: true, Agent: &core.AgentInfo{HostID: "fixture-host"},
	})
	if err != nil || result["error"] != nil || f.writes.Load() != 1 || f.syncs.Load() != 1 {
		t.Fatal("setup: real writer registration did not Write and Sync once", err, result)
	}
	if !f.writeBusy.Load() || !f.syncBusy.Load() || historyBusyThroughDoor(l) {
		t.Fatal("actual Append did not hold activity through Write/Sync and release before reply")
	}
	// Join the actual writer before taking ownership of its State. Then drive
	// the same ledger API's error exit with a real folded op and failed Sync.
	cancel()
	<-joined
	op := &core.Op{Kind: core.OpAckBoard, Token: "activity"}
	at := time.Now()
	if res, _, err := st.Apply(op, at); err != nil || res["error"] != nil {
		t.Fatal("setup: actual fold before failed append", err, res)
	}
	f.failSync.Store(true)
	if err := l.Append(st.Serial, at, op); !errors.Is(err, errHistoryActivitySync) {
		t.Fatal("setup: Sync failure did not reach Append", err)
	}
	if !f.writeBusy.Load() || !f.syncBusy.Load() || historyBusyThroughDoor(l) {
		t.Fatal("actual failed Append did not hold and release activity")
	}
}

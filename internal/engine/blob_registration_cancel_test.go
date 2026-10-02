package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

type pausedBlobLedger struct {
	memLedger
	entered chan struct{}
	proceed chan struct{}
	once    sync.Once
	failure error
}

func (l *pausedBlobLedger) Append(serial uint64, now time.Time, op *core.Op) error {
	if op.Kind == core.OpPutBlob {
		close(l.entered)
		<-l.proceed
		if l.failure != nil {
			return l.failure
		}
	}
	return l.memLedger.Append(serial, now, op)
}

func TestFailStopRegistrationReleasesItsRequestHold(t *testing.T) {
	for _, path := range []string{"commit", "direct"} {
		t.Run(path, func(t *testing.T) {
			store := newPausedReconcileStore(t)
			led := &pausedBlobLedger{
				entered: make(chan struct{}), proceed: make(chan struct{}),
				failure: errors.New("fixture: blob ledger persistence failed"),
			}
			e := New(core.NewState("fail-stop-registration", core.DefaultLimits()), led, nil)
			e.SetBlobs(store)
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan any, 1)
			go func() { defer func() { done <- recover() }(); e.Run(ctx) }()
			t.Cleanup(func() { store.resume(); led.resume(); cancel() })
			r, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "worker"})
			if err != nil || r["token"] == nil {
				t.Fatalf("register setup: %v %v", r, err)
			}
			token, size := r["token"].(string), int64(8)
			identity, err := e.AuthorizeUpload(ctx, token, hashHex([]byte("12345678")), &size)
			if err != nil {
				t.Fatal(err)
			}
			blob, _, err := store.Put([]byte("12345678"), 8)
			if err != nil {
				t.Fatal(err)
			}
			call, stopCall := context.WithCancel(ctx)
			defer stopCall()
			called := make(chan struct{})
			go func() {
				defer close(called)
				defer store.Release(blob)
				if path == "commit" {
					_, _ = e.CommitTransfer(call, identity, blob, 8, "text/plain")
				} else {
					_, _ = e.Do(call, &core.Op{Kind: core.OpPutBlob, Token: token, Blob: blob, Size: 8})
				}
			}()
			select {
			case <-led.entered:
			case <-time.After(2 * time.Second):
				t.Fatal("registration did not reach failing production ledger")
			}
			// The writer must release its request hold even when it never
			// reaches the post-append snapshot handoff or produces a reply.
			store.resume()
			led.resume()
			select {
			case fault := <-done:
				if !strings.Contains(fmt.Sprint(fault), "ledger persistence failure") {
					t.Fatalf("fail-stop panic was lost: %v", fault)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("fail-stop writer did not terminate")
			}
			stopCall()
			<-called
			if n, err := store.Store.Reconcile(map[string]bool{}); err != nil || n != 1 {
				t.Fatalf("failed writer leaked request protection: removed %d: %v", n, err)
			}
		})
	}
}

func (l *pausedBlobLedger) resume() { l.once.Do(func() { close(l.proceed) }) }

func TestCanceledRegistrationCannotExposeBytesBeforeLedgerCommit(t *testing.T) {
	for _, path := range []string{"commit", "direct"} {
		t.Run(path, func(t *testing.T) {
			store := newPausedReconcileStore(t)
			led := &pausedBlobLedger{entered: make(chan struct{}), proceed: make(chan struct{})}
			e := New(core.NewState("cancel-registration", core.DefaultLimits()), led, nil)
			e.SetBlobs(store)
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() { e.Run(ctx); close(done) }()
			t.Cleanup(func() { store.resume(); led.resume(); cancel(); <-done })
			r, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "worker"})
			if err != nil || r["token"] == nil {
				t.Fatalf("register setup: %v %v", r, err)
			}
			token := r["token"].(string)
			select {
			case <-store.entered:
			case <-time.After(2 * time.Second):
				t.Fatal("startup prune did not enter")
			}
			size := int64(8)
			identity, err := e.AuthorizeUpload(ctx, token, hashHex([]byte("12345678")), &size)
			if err != nil {
				t.Fatal(err)
			}
			blob, _, err := store.Put([]byte("12345678"), 8)
			if err != nil {
				t.Fatal(err)
			}
			call, stopCall := context.WithCancel(ctx)
			defer stopCall()
			returned := make(chan error, 1)
			go func() {
				defer store.Release(blob) // the caller's original stage hold
				var err error
				if path == "commit" {
					_, err = e.CommitTransfer(call, identity, blob, 8, "text/plain")
				} else {
					_, err = e.Do(call, &core.Op{Kind: core.OpPutBlob, Token: token, Blob: blob, Size: 8})
				}
				returned <- err
			}()
			select {
			case <-led.entered:
			case <-time.After(2 * time.Second):
				t.Fatal("registration never reached the production ledger boundary")
			}
			stopCall()
			select {
			case err := <-returned:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancel: %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("caller did not observe cancellation")
			}
			select {
			case id := <-store.released:
				if id != blob {
					t.Fatal("wrong caller hold released")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("caller did not release its own stage")
			}
			store.resume()
			select {
			case <-store.done:
			case <-time.After(2 * time.Second):
				t.Fatal("old snapshot did not finish")
			}
			led.resume()
			if _, err := e.GetBlob(ctx, token, blob, "inline"); err != nil {
				t.Fatalf("cancellation dropped stage protection before committed registration: %v", err)
			}
		})
	}
}

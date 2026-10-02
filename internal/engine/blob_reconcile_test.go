package engine

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/blobstore"
	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/ledger"
)

// Pause the REAL startup reconcile at the adapter boundary, after the writer
// captured its live-ID snapshot. No manual engine flag or synthetic hold.
type pausedReconcileStore struct {
	*blobstore.Store
	entered  chan map[string]bool
	proceed  chan struct{}
	done     chan struct{}
	released chan string
	once     sync.Once
	failure  string
}

func (s *pausedReconcileStore) Reconcile(live map[string]bool) (int, error) {
	s.entered <- live
	<-s.proceed
	defer close(s.done)
	n, err := s.Store.Reconcile(live)
	if err != nil {
		return n, err
	}
	switch s.failure {
	case "error":
		return n, errors.New("fixture: failure after a prune step")
	case "panic":
		panic("fixture: adapter panic after a prune step")
	}
	return n, nil
}

func TestReconcileFailureDoesNotLeakRegistrationHolds(t *testing.T) {
	for _, failure := range []string{"error", "panic"} {
		t.Run(failure, func(t *testing.T) {
			store := newPausedReconcileStore(t)
			store.failure = failure
			e, token, ctx := transferEngineFixture(t, store)
			t.Cleanup(store.resume)
			select {
			case <-store.entered:
			case <-time.After(2 * time.Second):
				t.Fatal("startup reconcile did not enter")
			}
			result, err := e.PutBlob(ctx, token, []byte("12345678"), "", "text/plain")
			if err != nil {
				t.Fatalf("registration setup: %v", err)
			}
			blob := result["blob"].(string)
			// Both the original stage and the writer's request hold must be
			// released before the snapshot hold's later completion receipt.
			for range 2 {
				select {
				case id := <-store.released:
					if id != blob {
						t.Fatal("wrong stage hold released")
					}
				case <-time.After(2 * time.Second):
					t.Fatal("caller or writer leaked its stage hold")
				}
			}
			store.resume()
			select {
			case <-store.done:
			case <-time.After(2 * time.Second):
				t.Fatal("failing reconcile did not finish")
			}
			if _, err := store.Read(blob); err != nil {
				t.Fatalf("failure deleted live bytes: %v", err)
			}
			// The underlying real adapter can now prune an absent ID: the
			// worker's failure must not leave an extra hold permanently active.
			select {
			case id := <-store.released:
				if id != blob {
					t.Fatal("worker released the wrong registration hold")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("failed worker leaked its registration hold")
			}
			if n, err := store.Store.Reconcile(map[string]bool{}); err != nil || n != 1 {
				t.Fatalf("failed worker retained protection: %d %v", n, err)
			}
		})
	}
}

func TestShutdownWaitsForStalePruneBeforeReleasingRegistrationHolds(t *testing.T) {
	store := newPausedReconcileStore(t)
	e := New(core.NewState("reconcile-cancel", core.DefaultLimits()), &memLedger{}, nil)
	e.SetBlobs(store)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.Run(ctx); close(done) }()
	t.Cleanup(func() { store.resume(); cancel(); <-done })
	result, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "worker"})
	if err != nil || result["token"] == nil {
		t.Fatalf("register setup: %v %v", result, err)
	}
	select {
	case <-store.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("startup reconcile did not enter")
	}
	result, err = e.PutBlob(ctx, result["token"].(string), []byte("12345678"), "", "text/plain")
	if err != nil {
		t.Fatalf("registration setup: %v", err)
	}
	blob := result["blob"].(string)
	cancel()
	select {
	case <-done:
		t.Fatal("shutdown returned while a stale prune could still delete live bytes")
	case <-time.After(30 * time.Millisecond):
	}
	store.resume()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown did not finish after prune returned")
	}
	if _, err := store.Read(blob); err != nil {
		t.Fatalf("canceled worker deleted committed bytes: %v", err)
	}
	if n, err := store.Store.Reconcile(map[string]bool{}); err != nil || n != 1 {
		t.Fatalf("shutdown leaked a registration hold: %d %v", n, err)
	}
}

func (s *pausedReconcileStore) resume() { s.once.Do(func() { close(s.proceed) }) }

func (s *pausedReconcileStore) Release(id string) {
	s.Store.Release(id)
	s.released <- id
}

func newPausedReconcileStore(t *testing.T) *pausedReconcileStore {
	t.Helper()
	dir := t.TempDir()
	box, err := ledger.LoadOrCreateKey(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	store, err := blobstore.New(dir, box)
	if err != nil {
		t.Fatal(err)
	}
	return &pausedReconcileStore{
		Store: store, entered: make(chan map[string]bool, 1),
		proceed: make(chan struct{}), done: make(chan struct{}), released: make(chan string, 8),
	}
}

func TestStartupReconcileCannotDeleteANewlyRegisteredBlob(t *testing.T) {
	store := newPausedReconcileStore(t)
	e, token, ctx := transferEngineFixture(t, store)
	t.Cleanup(store.resume)
	select {
	case live := <-store.entered:
		if len(live) != 0 {
			t.Fatal("startup snapshot unexpectedly includes later blobs")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not enter production startup reconcile")
	}
	plain := []byte("12345678")
	result, err := e.PutBlob(ctx, token, plain, "", "text/plain")
	if err != nil {
		t.Fatalf("registration setup: %v", err)
	}
	blob, ok := result["blob"].(string)
	if !ok {
		t.Fatalf("registration did not commit: %v", result)
	}
	// PutBlob has returned, so its caller's last in-flight hold was released.
	store.resume()
	select {
	case <-store.done:
	case <-time.After(2 * time.Second):
		t.Fatal("startup prune did not finish")
	}
	got, err := e.GetBlob(context.Background(), token, blob, "inline")
	if err != nil || !bytes.Equal(got["bytes"].([]byte), plain) {
		t.Fatalf("stale startup snapshot deleted live committed bytes: %v", err)
	}
}

// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

func TestDirectBlobRegistrationInheritsSnapshotProtection(t *testing.T) {
	store := newPausedReconcileStore(t)
	e, token, ctx := transferEngineFixture(t, store)
	t.Cleanup(store.resume)
	select {
	case <-store.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("startup reconcile did not enter")
	}
	id, size, err := store.Put([]byte("12345678"), 8)
	if err != nil {
		t.Fatalf("stage setup: %v", err)
	}
	_, err = e.Do(ctx, &core.Op{Kind: core.OpPutBlob, Token: token, Blob: id, Size: size, Mime: "text/plain"})
	store.Release(id)
	if err != nil {
		t.Fatalf("direct registration setup: %v", err)
	}
	store.resume()
	select {
	case <-store.done:
	case <-time.After(2 * time.Second):
		t.Fatal("prune did not finish")
	}
	if _, err := e.GetBlob(ctx, token, id, "inline"); err != nil {
		t.Fatalf("a caller bypassing CommitTransfer lost live bytes: %v", err)
	}
}

type overlappingReconcileStore struct {
	*pausedReconcileStore
	second *pausedReconcileStore
	calls  atomic.Int32
}

func (s *overlappingReconcileStore) Reconcile(live map[string]bool) (int, error) {
	if s.calls.Add(1) == 1 {
		return s.pausedReconcileStore.Reconcile(live)
	}
	return s.second.Reconcile(live)
}

func TestRegistrationHoldOutlivesEveryOverlappingSnapshot(t *testing.T) {
	first := newPausedReconcileStore(t)
	second := &pausedReconcileStore{
		Store: first.Store, entered: make(chan map[string]bool, 1),
		proceed: make(chan struct{}), done: make(chan struct{}), released: make(chan string, 8),
	}
	store := &overlappingReconcileStore{pausedReconcileStore: first, second: second}
	e, token, ctx := transferEngineFixture(t, store)
	t.Cleanup(func() { first.resume(); second.resume() })
	select {
	case <-first.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("startup reconcile did not enter")
	}
	// Trigger the SAME writer-owned routine the periodic Run branch calls;
	// never set its active-count or held-ID flags by hand.
	if _, err := e.query(ctx, func() core.Result { e.reconcileBlobs(); return nil }); err != nil {
		t.Fatal(err)
	}
	select {
	case <-second.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("overlapping reconcile did not enter")
	}
	result, err := e.PutBlob(ctx, token, []byte("12345678"), "", "text/plain")
	if err != nil {
		t.Fatalf("registration setup: %v", err)
	}
	blob := result["blob"].(string)
	first.resume()
	<-first.done
	// Observe the receipt on the writer, not merely the adapter's return:
	// otherwise the second prune could race ahead of a premature hold release.
	wait, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	for {
		res, err := e.query(wait, func() core.Result { return core.Result{"active": e.blobReconciles} })
		if err != nil {
			t.Fatalf("first completion receipt missing: %v", err)
		}
		if res["active"] == 1 {
			break
		}
	}
	second.resume()
	select {
	case <-second.done:
	case <-time.After(2 * time.Second):
		t.Fatal("second prune did not finish")
	}
	if _, err := e.GetBlob(ctx, token, blob, "inline"); err != nil {
		t.Fatalf("first receipt released protection before the last snapshot: %v", err)
	}
}

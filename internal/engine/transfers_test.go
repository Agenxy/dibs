package engine

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

type transferStore struct {
	*memBlobs
	free             uint64
	entered, proceed chan struct{}
	once             sync.Once
}

func (s *transferStore) FreeBytes() (uint64, error) {
	if s.entered != nil {
		s.once.Do(func() { close(s.entered); <-s.proceed })
	}
	return s.free, nil
}

func transferEngineFixture(t *testing.T, store Store) (*Engine, string, context.Context) {
	t.Helper()
	limits := core.DefaultLimits()
	limits.MaxBlobSize, limits.PerAgentBlobBytes, limits.BlobStoreBytes = 8, 8, 8
	e := New(core.NewState("transfer-engine", limits), &memLedger{}, nil)
	e.SetBlobs(store)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	r, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "worker"})
	if err != nil || r["token"] == nil {
		t.Fatalf("register setup: %v %v", r, err)
	}
	return e, r["token"].(string), ctx
}

func TestTransferStagingPreservesFullOwnerDedupAndEviction(t *testing.T) {
	e, token, ctx := transferEngineFixture(t, newMemBlobs())
	plain := []byte("12345678")
	stored, err := e.PutBlob(ctx, token, plain, "", "text/plain")
	if err != nil {
		t.Fatal(err)
	}
	// A full registry must not reject staging before existing dedup can run.
	if _, err := e.PutBlob(ctx, token, plain, "", "text/plain"); err != nil {
		t.Fatalf("full-owner legacy dedup changed: %v", err)
	}
	size := int64(len(plain))
	id, err := e.AuthorizeUpload(ctx, token, hashHex(plain), &size)
	if err != nil {
		t.Fatalf("proven dedup charged ownership twice: %v", err)
	}
	if err := e.ReleaseTransfer(ctx, id.Reservation); err != nil {
		t.Fatal(err)
	}
	if _, err := e.AuthorizeUpload(ctx, token, hashHex([]byte("other123")), &size); !errors.Is(err, core.ErrQuota) {
		t.Fatalf("future ownership not checked: %v", err)
	}
	// A different owner can replace an unreferenced full registry object by
	// the OLD deterministic eviction rule; staging is a separate bound.
	r, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "second"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.PutBlob(ctx, r["token"].(string), []byte("other123"), "", "text/plain"); err != nil {
		t.Fatalf("registry cap prevented pre-eviction staging: %v", err)
	}
	_, err = e.query(ctx, func() core.Result {
		if e.state.Blobs[stored["blob"].(string)] != nil {
			t.Error("old cap eviction no longer ran")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestCancelledTransferAdmissionDoesNotStrandReservation(t *testing.T) {
	store := &transferStore{memBlobs: newMemBlobs(), free: 1 << 50, entered: make(chan struct{}), proceed: make(chan struct{})}
	e, token, ctx := transferEngineFixture(t, store)
	request, cancel := context.WithCancel(ctx)
	result := make(chan error, 1)
	size := int64(8)
	go func() { _, err := e.AuthorizeTransfer(request, token, "", &size); result <- err }()
	select {
	case <-store.entered:
	case <-time.After(time.Second):
		t.Fatal("admission did not reach filesystem boundary")
	}
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
	close(store.proceed)
	deadline := time.Now().Add(time.Second)
	for {
		empty := false
		if _, err := e.query(ctx, func() core.Result { empty = len(e.transfers) == 0; return nil }); err != nil {
			t.Fatal(err)
		}
		if empty {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("lost reply stranded staging bytes")
		}
		// Each query crosses the writer channel: wait on events, not wall-clock sleeps.
	}
	id, err := e.AuthorizeTransfer(ctx, token, "", &size)
	if err != nil {
		t.Fatalf("cancelled admission consumed quota: %v", err)
	}
	if err := e.ReleaseTransfer(ctx, id.Reservation); err != nil {
		t.Fatal(err)
	}
}

func TestTransferFreeSpaceAdmissionAndIdentityFence(t *testing.T) {
	e, token, ctx := transferEngineFixture(t, &transferStore{memBlobs: newMemBlobs(), free: 0})
	size := int64(8)
	_, err := e.AuthorizeTransfer(ctx, token, "", &size)
	var domain *core.Error
	if !errors.As(err, &domain) || domain.Code != "E_STAGING_SPACE" || domain.Hint == "" {
		t.Fatalf("disk admission: %v", err)
	}
	if _, err := e.query(ctx, func() core.Result {
		if len(e.transfers) != 0 {
			t.Error("failed disk admission stranded reservation")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Never replace a port on a running engine: the off-thread reconcile owns
	// the original adapter too. A second real fixture models available space.
	e, token, ctx = transferEngineFixture(t, newMemBlobs())
	id, err := e.AuthorizeTransfer(ctx, token, "", &size)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.query(ctx, func() core.Result { e.state.Agents[id.Agent].CreatedSerial++; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := e.CheckTransfer(ctx, id, ""); !errors.Is(err, core.ErrBadToken) {
		t.Fatalf("replacement inherited ticket: %v", err)
	}
	if _, err := e.CommitTransfer(ctx, id, "sha256:"+hashHex([]byte("12345678")), size, "text/plain"); !errors.Is(err, core.ErrBadToken) {
		t.Fatalf("replacement committed old bytes: %v", err)
	}
}

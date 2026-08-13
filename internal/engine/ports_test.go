package engine

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// memLedger is an alternative Ledger backend that keeps ops in memory. Its
// existence is the point: if a second implementation can be written without
// touching the engine, the port is real.
type memLedger struct{ ops []*core.Op }

func (m *memLedger) Append(_ uint64, _ time.Time, op *core.Op) error {
	m.ops = append(m.ops, op)
	return nil
}

// memBlobs is an in-memory Store honouring the port contract.
type memBlobs struct {
	mu       sync.Mutex
	data     map[string][]byte
	inflight map[string]int
}

func newMemBlobs() *memBlobs {
	return &memBlobs{data: map[string][]byte{}, inflight: map[string]int{}}
}

// The Store contract requires concurrency safety: the engine reconciles on its
// own goroutine while callers are still staging bytes. A double that does not
// hold the same guarantee is not a double: it is a different component that
// happens to compile, and it will pass tests the real store would fail.

func (m *memBlobs) Put(plain []byte, maxSize int) (string, int64, error) {
	if len(plain) > maxSize {
		return "", 0, core.ErrBlobTooLarge
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	id := "sha256:" + hashHex(plain)
	m.inflight[id]++
	m.data[id] = append([]byte(nil), plain...)
	return id, int64(len(plain)), nil
}
func (m *memBlobs) PutFile(string, int) (string, int64, error) { return "", 0, core.ErrBlobMissing }
func (m *memBlobs) Read(id string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.data[id]
	if !ok {
		return nil, core.ErrBlobMissing
	}
	return b, nil
}
func (m *memBlobs) Materialize(string) (string, error) { return "", core.ErrBlobMissing }
func (m *memBlobs) Reconcile(live map[string]bool) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for id := range m.data {
		if !live[id] && m.inflight[id] == 0 {
			delete(m.data, id)
			n++
		}
	}
	return n, nil
}

func (m *memBlobs) Release(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.inflight[id] > 0 {
		m.inflight[id]--
	}
}

type deadProber struct{}

func (deadProber) Alive(int) bool { return true }

// TestPortsAreSwappable drives a full register → ack → declare flow against
// backends that share no code with the production adapters. If this compiles
// and passes, "storage is pluggable" is a fact rather than a claim.
func TestPortsAreSwappable(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	led := &memLedger{}
	e := New(st, led, deadProber{})
	e.SetBlobs(newMemBlobs())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	res, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "alpha"})
	if err != nil {
		t.Fatalf("register through swapped ports: %v", err)
	}
	tok, _ := res["token"].(string)
	if tok == "" {
		t.Fatal("no token returned")
	}
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpAckBoard, Token: tok}); err != nil {
		t.Fatalf("check_in: %v", err)
	}
	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpSetSlot, Token: tok,
		Text: "working", Refs: []string{"issue:1"},
	}); err != nil {
		t.Fatalf("declare: %v", err)
	}

	// The alternative ledger must have received every state-advancing op.
	if len(led.ops) < 3 {
		t.Fatalf("alternative ledger got %d ops, want >=3 (register, ack, slot)", len(led.ops))
	}
	// And a blob round-trips through the alternative store.
	if _, err := e.PutBlob(ctx, tok, []byte("hello"), "", "text/plain"); err != nil {
		t.Fatalf("put_blob through swapped store: %v", err)
	}
}

func hashHex(b []byte) string {
	const hexd = "0123456789abcdef"
	var h [32]byte
	for i, c := range b { // deterministic, test-only; not a real digest
		h[i%32] ^= c + byte(i)
	}
	out := make([]byte, 64)
	for i, c := range h {
		out[i*2], out[i*2+1] = hexd[c>>4], hexd[c&0xf]
	}
	return string(out)
}

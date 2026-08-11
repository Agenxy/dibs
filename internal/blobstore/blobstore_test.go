package blobstore

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/agenxy/dibs/internal/ledger"
)

func newStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	box, err := ledger.LoadOrCreateKey(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(dir, box)
	if err != nil {
		t.Fatal(err)
	}
	return s, dir
}

const cap = 64 << 20

// TestPutReadRoundTrip: content-addressed store, exact byte round-trip, and
// idempotent re-put (same id, no error).
func TestPutReadRoundTrip(t *testing.T) {
	s, _ := newStore(t)
	content := []byte("the generated artifact\x00\x01binary")
	id, size, err := s.Put(content, cap)
	if err != nil {
		t.Fatal(err)
	}
	if size != int64(len(content)) {
		t.Fatalf("size=%d want %d", size, len(content))
	}
	if !strings.HasPrefix(id, "sha256:") || len(id) != len("sha256:")+64 {
		t.Fatalf("bad id %q", id)
	}
	got, err := s.Read(id)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, content) {
		t.Fatal("round-trip mismatch")
	}
	// Idempotent re-put returns the same id.
	id2, _, err := s.Put(content, cap)
	if err != nil || id2 != id {
		t.Fatalf("re-put id=%q err=%v, want same id", id2, err)
	}
}

// TestEncryptedAtRest: the on-disk blob file is ciphertext, not the plaintext
// (A3). The id (a hash) is a safe filename.
func TestEncryptedAtRest(t *testing.T) {
	s, dir := newStore(t)
	secret := []byte("TOP-SECRET-PLAINTEXT-PAYLOAD")
	id, _, err := s.Put(secret, cap)
	if err != nil {
		t.Fatal(err)
	}
	h := id[len("sha256:"):]
	raw, err := os.ReadFile(filepath.Join(dir, "blobs", h[:2], h))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, secret) {
		t.Fatal("plaintext found in at-rest blob file")
	}
}

// TestFileMode: blob and materialized files are 0600, dirs 0700 (A3/A8.1).
func TestFileMode(t *testing.T) {
	s, dir := newStore(t)
	id, _, _ := s.Put([]byte("x"), cap)
	h := id[len("sha256:"):]
	fi, err := os.Stat(filepath.Join(dir, "blobs", h[:2], h))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("blob file mode %v, want 0600", fi.Mode().Perm())
	}
	p, err := s.Materialize(id)
	if err != nil {
		t.Fatal(err)
	}
	mfi, _ := os.Stat(p)
	if mfi.Mode().Perm() != 0o600 {
		t.Fatalf("materialized file mode %v, want 0600", mfi.Mode().Perm())
	}
}

// TestIDValidationBlocksTraversal: a malformed/hostile id never reaches the
// filesystem (P2-3).
func TestIDValidationBlocksTraversal(t *testing.T) {
	s, _ := newStore(t)
	for _, bad := range []string{"sha256:../../etc/passwd", "sha256:..", "../x", "sha256:GG"} {
		if _, err := s.Read(bad); err == nil {
			t.Fatalf("Read(%q) should reject malformed id", bad)
		}
		if _, err := s.Materialize(bad); err == nil {
			t.Fatalf("Materialize(%q) should reject malformed id", bad)
		}
	}
}

// TestReconcileDropsOrphans: files with no live registry id are deleted; live
// ones are kept (P0-1 orphan reclamation / A5 eviction cleanup).
func TestReconcileDropsOrphans(t *testing.T) {
	s, _ := newStore(t)
	keep, _, _ := s.Put([]byte("keep me"), cap)
	drop, _, _ := s.Put([]byte("orphan me"), cap)
	// Simulate registration completing for both (ends in-flight protection).
	s.Release(keep)
	s.Release(drop)
	_, _ = s.Materialize(drop) // also leaves an out/ file to reclaim

	n, err := s.Reconcile(map[string]bool{keep: true})
	if err != nil {
		t.Fatal(err)
	}
	if n < 1 {
		t.Fatalf("reconcile removed %d, want >=1", n)
	}
	if _, err := s.Read(keep); err != nil {
		t.Fatal("live blob wrongly removed")
	}
	if _, err := s.Read(drop); !errors.Is(err, ErrMissing) {
		t.Fatalf("orphan blob should be gone: %v", err)
	}
}

// TestReconcileSkipsInFlight is the P0 regression guard: a blob whose bytes are
// staged but not yet registered (held in-flight) must survive a Reconcile that
// snapshots a live set NOT yet containing it: otherwise a concurrent reconcile
// would turn a live put into a permanent registry-without-bytes.
func TestReconcileSkipsInFlight(t *testing.T) {
	s, _ := newStore(t)
	id, _, err := s.Put([]byte("in flight, not yet registered"), cap) // holds id
	if err != nil {
		t.Fatal(err)
	}
	// Reconcile with an EMPTY live set (registration hasn't happened yet).
	if _, err := s.Reconcile(map[string]bool{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Read(id); err != nil {
		t.Fatalf("in-flight blob was wrongly reaped by reconcile: %v", err)
	}
	// After registration completes (Release) and it's still not live, it's an
	// orphan and reconcile may reclaim it.
	s.Release(id)
	if _, err := s.Reconcile(map[string]bool{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Read(id); !errors.Is(err, ErrMissing) {
		t.Fatalf("released non-live blob should be reclaimable: %v", err)
	}
}

// TestConcurrentPutVsReconcile reproduces the exact P0 interleaving under load:
// while a reconciler goroutine hammers Reconcile with a live set that never
// contains the in-flight ids, many concurrent puts must each still read back
// their bytes for the whole window between Put and Release. Run with -race.
func TestConcurrentPutVsReconcile(t *testing.T) {
	s, _ := newStore(t)
	stop := make(chan struct{})
	var recon sync.WaitGroup
	recon.Add(1)
	go func() {
		defer recon.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_, _ = s.Reconcile(map[string]bool{}) // nothing "live": worst case
			}
		}
	}()

	var wg sync.WaitGroup
	var failures int64
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			content := []byte(fmt.Sprintf("concurrent-put-%d", n))
			id, _, err := s.Put(content, cap)
			if err != nil {
				atomic.AddInt64(&failures, 1)
				return
			}
			// Simulate the [staged, registering] window: the bytes MUST be
			// readable here despite the reconciler running flat out.
			if got, rerr := s.Read(id); rerr != nil || !bytes.Equal(got, content) {
				atomic.AddInt64(&failures, 1)
			}
			s.Release(id)
		}(i)
	}
	wg.Wait()
	close(stop)
	recon.Wait()
	if failures != 0 {
		t.Fatalf("%d in-flight blobs were lost to a concurrent reconcile", failures)
	}
}

// TestReconcileSkipsTempFiles: an in-progress .tmp-* write is never deleted.
func TestReconcileSkipsTempFiles(t *testing.T) {
	s, dir := newStore(t)
	shard := filepath.Join(dir, "blobs", "ab")
	if err := os.MkdirAll(shard, 0o700); err != nil {
		t.Fatal(err)
	}
	tmp := filepath.Join(shard, tmpPrefix+"active-write")
	if err := os.WriteFile(tmp, []byte("mid-write"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reconcile(map[string]bool{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(tmp); err != nil {
		t.Fatalf("reconcile deleted an in-progress temp file: %v", err)
	}
}

// TestPutFileRejectsNonRegular: a fifo/dir path can't drive an unbounded read.
func TestPutFileRejectsDir(t *testing.T) {
	s, dir := newStore(t)
	if _, _, err := s.PutFile(dir, cap); err == nil {
		t.Fatal("PutFile on a directory should error")
	}
}

// TestPutRejectsOversize enforces the byte cap during staging.
func TestPutRejectsOversize(t *testing.T) {
	s, _ := newStore(t)
	if _, _, err := s.Put(make([]byte, 11), 10); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("oversize put: %v, want ErrTooLarge", err)
	}
}

// TestReadMissing maps an absent-but-valid id to ErrMissing (→ E_BLOB_UNAVAILABLE).
func TestReadMissing(t *testing.T) {
	s, _ := newStore(t)
	// valid-format id that was never stored
	id := "sha256:" + strings.Repeat("ab", 32)
	if _, err := s.Read(id); !errors.Is(err, ErrMissing) {
		t.Fatalf("Read(absent)=%v, want ErrMissing", err)
	}
}

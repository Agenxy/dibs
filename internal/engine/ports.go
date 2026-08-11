package engine

import (
	"time"

	"github.com/agenxy/lanes/internal/core"
)

// Ports: the engine depends on these interfaces, never on concrete
// implementations. Adapters live in their own packages (internal/ledger,
// internal/blobstore) and are injected at wiring time in cmd/lanesd.
//
// Interfaces are declared here, in the *consumer*, which is the Go idiom: the
// engine states what it needs, and any implementation satisfying that shape is
// a valid backend. Adding a backend is additive: no core change.
//
// Deliberately NOT declared yet: Index and Embedder. They arrive with semantic
// discovery, together with a real consumer. An interface with no caller is
// speculative surface, and PHILOSOPHY.md rule 7 says every component earns its
// place.

// Ledger is durable, ordered, append-only truth. The engine appends exactly
// when an op advanced the serial, so an implementation may assume serials
// arrive strictly increasing and gap-free.
//
// Persistence failure is fail-stop: returning an error causes the daemon to
// panic rather than continue with state that is not on disk (SPEC §4). An
// adapter must not silently drop or reorder.
type Ledger interface {
	Append(serial uint64, ts time.Time, op *core.Op) error
}

// Store holds attachment bytes outside the replay model (the blob store,
// named for what it is, not what it holds; e.blobs supplies the context). The ledger records
// which blobs *should* exist; this holds the bytes themselves.
//
// Contract an adapter must honour (SPEC-ATTACHMENTS A4.1):
//   - Put/PutFile make bytes durable BEFORE returning, so the caller can safely
//     ledger the reference afterwards. A crash may orphan bytes; it must never
//     leave a registry entry with no bytes.
//   - Put/PutFile hold the returned id "in flight" until Release, so a
//     concurrent Reconcile cannot delete bytes that are staged but not yet
//     registered.
//   - Reconcile deletes only ids absent from live and not in flight.
//
// Implementations MUST be safe for concurrent use. The engine reconciles on its
// own goroutine (see reconcileBlobs) while callers are still staging bytes, so
// Put and Reconcile genuinely overlap. This was implied by the in-flight rule
// above but never stated, and the in-package test double was written without a
// lock as a result: caught by `go test -race`, which is why it is spelled out
// here rather than left to be inferred.
type Store interface {
	// Put stages plaintext bytes, returning their content id and size.
	Put(plain []byte, maxSize int) (id string, size int64, err error)
	// PutFile stages a regular file's contents. Must refuse non-regular files.
	PutFile(path string, maxSize int) (id string, size int64, err error)
	// Read returns decrypted bytes, or an error the engine maps to
	// E_BLOB_UNAVAILABLE when the bytes are gone.
	Read(id string) ([]byte, error)
	// Materialize writes decrypted bytes to a file and returns its path.
	Materialize(id string) (string, error)
	// Reconcile removes stored objects whose id is not in live. Returns the
	// number removed.
	Reconcile(live map[string]bool) (int, error)
	// Release ends the in-flight hold taken by Put/PutFile.
	Release(id string)
}

// Prober answers PID liveness (impure; verdicts are recorded into sweep ops so
// replay reproduces the decision rather than re-probing).
type Prober interface {
	Alive(pid int) bool
}

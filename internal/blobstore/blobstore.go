// Package blobstore is the content-addressed side store for attachment bytes
// (SPEC-ATTACHMENTS A3). It lives beside the ledger but outside the replay
// model: the ledger records which blobs *should* exist (the core registry), and
// this store holds the encrypted bytes. All byte work — hashing, sealing,
// reading, decrypting, materializing — runs OFF the single-writer event loop,
// so a large or hostile input can never stall coordination (fixes P0-2).
//
// Durability discipline (fixes P0-1/P2-1): Put writes the sealed file, fsyncs
// it, renames it into place, and fsyncs the directory BEFORE the caller ledgers
// the registry entry — git's object-before-ref order. A crash between the two
// leaves a harmless orphan file (no registry entry), never a registry entry
// with no bytes. Reconcile deletes orphans (and evicted files) by diffing the
// filesystem against the live registry.
package blobstore

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/agenxy/lanes/internal/core"
	"github.com/agenxy/lanes/internal/ledger"
)

// tmpPrefix marks in-progress writes; reconcile never deletes them.
const tmpPrefix = ".tmp-"

// ErrMissing means the registry says a blob exists but its bytes are gone
// (evicted or never durably written). Callers map it to E_BLOB_UNAVAILABLE.
var ErrMissing = core.ErrBlobMissing

// ErrTooLarge means the source exceeded the byte cap during staging.
var ErrTooLarge = core.ErrBlobTooLarge

// Store is the on-disk blob store rooted at a data dir.
type Store struct {
	blobsDir string
	outDir   string
	box      *ledger.Box

	// inflight guards the [bytes-on-disk, registry-recorded] window of a put:
	// a staged blob's id lives here until the caller registers it and calls
	// Release, so a concurrent Reconcile (which sees the id as "not yet live")
	// cannot delete bytes an in-flight put just wrote. Without this, the 30s
	// reconcile could turn a live put into a permanent registry-without-bytes.
	mu       sync.Mutex
	inflight map[string]int
}

// New opens (creating dirs 0700) a store under root. box seals bytes at rest.
func New(root string, box *ledger.Box) (*Store, error) {
	s := &Store{
		blobsDir: filepath.Join(root, "blobs"),
		outDir:   filepath.Join(root, "out"),
		box:      box,
		inflight: map[string]int{},
	}
	for _, d := range []string{s.blobsDir, s.outDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, err
		}
	}
	s.sweepAbandonedTemps()
	return s, nil
}

// sweepAbandonedTemps deletes leftover .tmp-* files at startup.
//
// atomicWrite stages through a temp file and removes it on every error path,
// but a process killed between the write and the rename leaves one behind — and
// Reconcile is required to skip .tmp-* forever, because at runtime it cannot
// tell an abandoned temp from a write that is happening right now. Its comment
// said "active/abandoned temp write — never an orphan to reap", which described
// the abandoned half as intentional; nothing ever reaped them.
//
// Two consequences, and the second is the serious one. They accumulate without
// bound, so out/ is not in fact bounded by the blob store as SPEC-ATTACHMENTS
// claims. And a materialization temp holds PLAINTEXT: it outlives the eviction
// of the encrypted blob it came from, which is precisely what "a decrypted copy
// can never outlive the encrypted blob" exists to prevent, and it makes blob
// TTL meaningless for anything that crashed at the wrong moment.
//
// Startup is the one moment this is unambiguous. The daemon holds the directory
// lock and has just started, so no write can be in flight and every temp
// present is by definition abandoned — no age heuristic, no race with a
// concurrent Put. Best-effort: a temp we cannot delete is not a reason to
// refuse to boot.
func (s *Store) sweepAbandonedTemps() {
	// out/ is flat; blobs/ is sharded one level, and temps stage inside the
	// shard beside the file they will become.
	dirs := []string{s.outDir, s.blobsDir}
	if shards, err := os.ReadDir(s.blobsDir); err == nil {
		for _, sh := range shards {
			if sh.IsDir() {
				dirs = append(dirs, filepath.Join(s.blobsDir, sh.Name()))
			}
		}
	}
	for _, dir := range dirs {
		removeTempsIn(dir)
	}
}

func removeTempsIn(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), tmpPrefix) {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

func (s *Store) hold(id string) {
	s.mu.Lock()
	s.inflight[id]++
	s.mu.Unlock()
}

// Release ends the in-flight protection for a staged blob id. The caller MUST
// call this after registering the blob (or after registration fails), exactly
// once per successful Put/PutFile.
func (s *Store) Release(id string) {
	s.mu.Lock()
	if s.inflight[id] > 1 {
		s.inflight[id]--
	} else {
		delete(s.inflight, id)
	}
	s.mu.Unlock()
}

func (s *Store) staging(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inflight[id] > 0
}

// blobPath maps an id to its sharded path AFTER strict validation, so no
// unvalidated id byte ever reaches filepath.Join (fixes P2-3 traversal).
func (s *Store) blobPath(id string) (string, error) {
	if !core.ValidBlobID(id) {
		return "", core.ErrBadID
	}
	h := id[len("sha256:"):]
	return filepath.Join(s.blobsDir, h[:2], h), nil
}

func (s *Store) outPath(id string) (string, error) {
	if !core.ValidBlobID(id) {
		return "", core.ErrBadID
	}
	return filepath.Join(s.outDir, id[len("sha256:"):]), nil
}

// idOf returns the content address of plaintext.
func idOf(plain []byte) string {
	sum := sha256.Sum256(plain)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Put stages plaintext bytes durably and returns their content id and size.
// Idempotent: identical content re-put is a no-op write (the file is already
// there and, being content-addressed, correct). Fully off-thread. On success
// the id is held in-flight; the caller MUST Release(id) after registering it.
func (s *Store) Put(plain []byte, maxSize int) (id string, size int64, err error) {
	if len(plain) > maxSize {
		return "", 0, ErrTooLarge
	}
	id = idOf(plain)
	// Hold BEFORE writing: the id is known up front, so reconcile is excluded
	// from the moment bytes could appear on disk until the caller releases.
	s.hold(id)
	committed := false
	defer func() {
		if !committed {
			s.Release(id) // failed: don't leak the hold
		}
	}()
	dst, err := s.blobPath(id)
	if err != nil {
		return "", 0, err
	}
	if fi, statErr := os.Stat(dst); statErr == nil && fi.Mode().IsRegular() {
		committed = true
		return id, int64(len(plain)), nil // already durable (content-addressed)
	}
	sealed, err := s.box.SealBytes(plain)
	if err != nil {
		return "", 0, err
	}
	if err := s.atomicWrite(dst, sealed); err != nil {
		return "", 0, err
	}
	committed = true
	return id, int64(len(plain)), nil
}

// PutFile reads a regular file (bounded, off-thread) and stages it as a blob.
// It refuses non-regular files (pipes, devices) so a fileref-style path cannot
// turn into an unbounded read on the store side either.
func (s *Store) PutFile(path string, maxSize int) (id string, size int64, err error) {
	// The daemon reading an agent-named local file is the intended `path=`
	// ingest (same same-machine trust model as filerefs/claims); bounded below
	// by a regular-file check + size cap.
	f, err := os.Open(path) //nolint:gosec // G304: agent-supplied path is the feature
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return "", 0, err
	}
	if !fi.Mode().IsRegular() {
		return "", 0, fmt.Errorf("not a regular file: %s", path)
	}
	if fi.Size() > int64(maxSize) {
		return "", 0, ErrTooLarge
	}
	// LimitReader guards against races where the file grows after the stat.
	plain, err := io.ReadAll(io.LimitReader(f, int64(maxSize)+1))
	if err != nil {
		return "", 0, err
	}
	return s.Put(plain, maxSize)
}

// atomicWrite writes to a temp file, fsyncs it, renames into place, and fsyncs
// the parent dir — so the bytes are durable before the caller ledgers the ref.
func (s *Store) atomicWrite(dst string, data []byte) error {
	dir := filepath.Dir(dst)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = tmp.Close(); _ = os.Remove(tmpName) }
	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, dst); err != nil {
		cleanup()
		return err
	}
	return fsyncDir(dir)
}

func fsyncDir(dir string) error {
	d, err := os.Open(dir) //nolint:gosec // G304: dir is internally constructed, never user input
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	return d.Sync()
}

// Read returns the decrypted bytes of a blob, or ErrMissing if its file is gone.
func (s *Store) Read(id string) ([]byte, error) {
	p, err := s.blobPath(id)
	if err != nil {
		return nil, err
	}
	sealed, err := os.ReadFile(p) //nolint:gosec // G304: p derives from a strictly id-validated blobPath
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrMissing
		}
		return nil, err
	}
	return s.box.OpenBytes(sealed)
}

// Materialize writes a blob's decrypted bytes to out/<id> (0600) and returns
// the path — the context-hygiene delivery for large blobs (A8.1). The file is
// mode-locked and reconciled/evicted with its blob; it is never a 0644 leak.
func (s *Store) Materialize(id string) (string, error) {
	p, err := s.outPath(id)
	if err != nil {
		return "", err
	}
	if fi, statErr := os.Stat(p); statErr == nil && fi.Mode().IsRegular() {
		return p, nil // already materialized
	}
	plain, err := s.Read(id)
	if err != nil {
		return "", err
	}
	if err := s.atomicWrite(p, plain); err != nil {
		return "", err
	}
	return p, nil
}

// Reconcile deletes any file under blobs/ or out/ whose id is not in live —
// evicted blobs and crash orphans alike (fixes P0-1/P2-1). Returns the count
// removed. Runs off-thread; the caller supplies a snapshot of live ids.
func (s *Store) Reconcile(live map[string]bool) (int, error) {
	removed := 0
	shards, err := os.ReadDir(s.blobsDir)
	if err != nil && !os.IsNotExist(err) {
		return removed, err
	}
	for _, shard := range shards {
		if shard.IsDir() {
			removed += s.pruneDir(filepath.Join(s.blobsDir, shard.Name()), live)
		}
	}
	// out/<hex> — a materialized copy must never outlive its blob.
	removed += s.pruneDir(s.outDir, live)
	return removed, nil
}

// pruneDir removes files in dir whose id is neither live nor being staged;
// returns the count removed. It never touches an in-progress temp file (tmpPrefix)
// nor an id currently in-flight — the two ways a file can legitimately exist
// before it is registered in the live set.
func (s *Store) pruneDir(dir string, live map[string]bool) int {
	files, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	removed := 0
	for _, f := range files {
		name := f.Name()
		if f.IsDir() || strings.HasPrefix(name, tmpPrefix) {
			// A temp here may be a write in progress, and at runtime there is
			// no way to tell that from one abandoned by a crash: the names are
			// random, so they cannot be matched against the in-flight set.
			// Deleting a live one would corrupt a Put that is about to rename.
			// Abandoned temps are reaped at startup instead, by
			// sweepAbandonedTemps, where the answer is unambiguous.
			continue
		}
		id := "sha256:" + name
		if live[id] || s.staging(id) {
			continue
		}
		if (!core.ValidBlobID(id)) || !live[id] {
			if os.Remove(filepath.Join(dir, name)) == nil {
				removed++
			}
		}
	}
	return removed
}

package blobstore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agenxy/lanes/internal/ledger"
)

// A process killed between the temp write and the rename leaves a .tmp-* file,
// and nothing ever removed one.
//
// Reconcile must skip them: at runtime it cannot distinguish an abandoned temp
// from a Put that is midway through, because the names are random and cannot be
// matched against the in-flight set, so they accumulated for the life of the
// directory. out/ was therefore not bounded by the blob store, as
// SPEC-ATTACHMENTS claims, and a materialization temp holds PLAINTEXT: it
// outlived the eviction of the encrypted blob it came from, which is the exact
// thing "a decrypted copy can never outlive the encrypted blob" forbids.
func TestAbandonedTempsAreReapedAtStartup(t *testing.T) {
	root := t.TempDir()
	box, err := ledger.LoadOrCreateKey(filepath.Join(root, "key"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(root, box)
	if err != nil {
		t.Fatal(err)
	}

	// A real blob, so we can prove the sweep is selective.
	id, _, err := s.Put([]byte("real content"), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Materialize(id); err != nil {
		t.Fatal(err)
	}

	// Crash debris: plaintext temps in out/ and in a blobs/ shard.
	shards, err := os.ReadDir(s.blobsDir)
	if err != nil || len(shards) == 0 {
		t.Fatalf("expected a blobs shard, got %v (%v)", shards, err)
	}
	shard := filepath.Join(s.blobsDir, shards[0].Name())
	debris := []string{
		filepath.Join(s.outDir, tmpPrefix+"abandoned1"),
		filepath.Join(s.outDir, tmpPrefix+"abandoned2"),
		filepath.Join(shard, tmpPrefix+"abandoned3"),
	}
	for _, p := range debris {
		if err := os.WriteFile(p, []byte("PLAINTEXT-THAT-OUTLIVED-ITS-BLOB"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// Reconcile deliberately leaves them: it cannot tell abandoned from active.
	if _, err := s.Reconcile(map[string]bool{id: true}); err != nil {
		t.Fatal(err)
	}
	for _, p := range debris {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("Reconcile removed %s: it must not, a live write looks the same", p)
		}
	}

	// Restarting is the moment it is unambiguous: no write can be in flight.
	if _, err := New(root, box); err != nil {
		t.Fatal(err)
	}
	for _, p := range debris {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s survived a restart: plaintext debris accumulates forever "+
				"and outlives the blob it came from", p)
		}
	}

	// And the real content is untouched.
	got, err := s.Read(id)
	if err != nil {
		t.Fatalf("the sweep took a live blob with it: %v", err)
	}
	if string(got) != "real content" {
		t.Fatalf("blob content changed: %q", got)
	}
	outs, err := os.ReadDir(s.outDir)
	if err != nil {
		t.Fatal(err)
	}
	var kept []string
	for _, f := range outs {
		kept = append(kept, f.Name())
	}
	if len(kept) != 1 || strings.HasPrefix(kept[0], tmpPrefix) {
		t.Errorf("out/ should hold exactly the one materialized file, got %v", kept)
	}
}

// The adversarial cases, kept because the sweep deletes files and a sweep that
// deletes files must be exact about which. These were written against a review
// probe that lived in /tmp; pinning them here is what keeps the guarantees.
func TestTheTempSweepIsExact(t *testing.T) {
	root := t.TempDir()
	box, err := ledger.LoadOrCreateKey(filepath.Join(root, "key"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(root, box)
	if err != nil {
		t.Fatal(err)
	}
	id, _, err := s.Put([]byte("live blob"), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	materialized, err := s.Materialize(id)
	if err != nil {
		t.Fatal(err)
	}

	// A shard created AFTER the store was first opened. The sweep enumerates
	// shards at open time, so a shard that appeared since must still be swept
	// on the next start: this is the ordinary case, not an exotic one: shards
	// are created as blobs arrive.
	lateShard := filepath.Join(root, "blobs", "zz")
	if err := os.MkdirAll(lateShard, 0o700); err != nil {
		t.Fatal(err)
	}
	lateTemp := filepath.Join(lateShard, tmpPrefix+"late")
	if err := os.WriteFile(lateTemp, []byte("plain"), 0o600); err != nil {
		t.Fatal(err)
	}

	// A symlink named like a temp. Removing it must unlink the SYMLINK and
	// leave whatever it points at: following it would let a stale link in the
	// blob directory delete an arbitrary file the daemon can write.
	target := filepath.Join(root, "precious.txt")
	if err := os.WriteFile(target, []byte("must survive"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "out", tmpPrefix+"symlink")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	// Names that merely resemble the prefix. Sweeping by "contains" or by a
	// looser prefix would take these, and one of them is a real blob shard.
	nonTemps := []string{
		filepath.Join(root, "out", "tmp-no-leading-dot"),
		filepath.Join(root, "out", ".tmpfoo"),
		filepath.Join(root, "out", "x"+tmpPrefix+"embedded"),
	}
	for _, p := range nonTemps {
		if err := os.WriteFile(p, []byte("ordinary"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// A temp whose mode denies writing. Unlink permission belongs to the
	// DIRECTORY, not the file, so this is removable, and it is the realistic
	// stale-file case.
	readonly := filepath.Join(root, "out", tmpPrefix+"readonly")
	if err := os.WriteFile(readonly, []byte("plain"), 0o400); err != nil {
		t.Fatal(err)
	}

	if _, err := New(root, box); err != nil {
		t.Fatalf("the sweep refused to boot: %v", err)
	}

	for _, gone := range []string{lateTemp, link, readonly} {
		if _, err := os.Lstat(gone); !os.IsNotExist(err) {
			t.Errorf("%s survived the sweep", gone)
		}
	}
	if got, err := os.ReadFile(target); err != nil || string(got) != "must survive" {
		t.Errorf("the sweep followed a symlink and touched its target: %q %v", got, err)
	}
	for _, kept := range append(nonTemps, materialized) {
		if _, err := os.Stat(kept); err != nil {
			t.Errorf("the sweep took %s, which is not a staging temp: %v", kept, err)
		}
	}
	if got, err := s.Read(id); err != nil || string(got) != "live blob" {
		t.Errorf("live blob changed: %q %v", got, err)
	}
}

// Best-effort has a boundary and it should be written down rather than
// discovered: a directory the daemon cannot write keeps its debris, the daemon
// still boots, and the next restart after permissions recover clears it.
func TestADirectoryTheSweepCannotWriteDoesNotBlockBoot(t *testing.T) {
	root := t.TempDir()
	box, err := ledger.LoadOrCreateKey(filepath.Join(root, "key"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(root, box); err != nil {
		t.Fatal(err)
	}
	shard := filepath.Join(root, "blobs", "ro")
	if err := os.MkdirAll(shard, 0o700); err != nil {
		t.Fatal(err)
	}
	blocked := filepath.Join(shard, tmpPrefix+"blocked")
	if err := os.WriteFile(blocked, []byte("plain"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(shard, 0o500); err != nil { // readable, not writable
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(shard, 0o700) })

	if _, err := New(root, box); err != nil {
		t.Fatalf("an undeletable temp must not stop the daemon booting: %v", err)
	}
	if _, err := os.Stat(blocked); err != nil {
		t.Fatalf("precondition: the temp should still be there: the probe did not "+
			"actually block deletion: %v", err)
	}

	if err := os.Chmod(shard, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := New(root, box); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(blocked); !os.IsNotExist(err) {
		t.Errorf("debris survived a restart after permissions recovered: %v", err)
	}
}

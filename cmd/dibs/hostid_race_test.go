package main

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// Two bridges starting together on a fresh directory get ONE host id.
//
// The file was read, an id generated, and the file written, unlocked: each
// bridge kept its own id for its lifetime and the last write won on disk.
// The machine then had two identities, its own agents stopped colliding
// with each other, and the host bridge could not be reached for the agents
// carrying the losing id. Exclusive creation makes the loser read the
// winner's. Found by the pre-release review.
func TestConcurrentFirstStartsAgreeOnOneHostID(t *testing.T) {
	dir := t.TempDir()
	const n = 16
	ids := make([]string, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ids[i] = loadOrCreateHostID(dir)
		}()
	}
	wg.Wait()
	on, err := os.ReadFile(filepath.Join(dir, "host_id"))
	if err != nil {
		t.Fatal(err)
	}
	for i, id := range ids {
		if id == "" || id != string(on) {
			t.Fatalf("start %d answered %q while the file holds %q: one machine, two identities", i, id, on)
		}
	}
}

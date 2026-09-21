package main

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/agenxy/dibs/internal/supgang"
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

// A bridge that was about to mint an id takes one recorded meanwhile.
//
// The two branches of the first start on a fleet member are not
// exclusive with each other: both bridges find the directory empty, one
// Supgang lookup answers and the other times out, and the loser mints a
// random id and keeps it for its life. The machine then has two
// identities again, with none of the protection the exclusive
// publication of host_id gives two minters. Round forty of the
// pre-release review.
func TestAMintedIDNeverOverridesAnIdentityRecordedMeanwhile(t *testing.T) {
	const fleet = "a8a37e32c37cdf4fb7634de622bc3f84ccb4636580d1ea26af3fe8ac31d1f152"
	dir := t.TempDir()
	// The other bridge's lookup answered while this one was deciding.
	if err := os.WriteFile(filepath.Join(dir, supgang.NodeIDFile), []byte(fleet+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := loadOrCreateHostID(dir); got != fleet {
		t.Fatalf("the minting branch answered %q while this machine is recorded as %q: "+
			"one computer, two identities, and its agents stop colliding with each other", got, fleet)
	}
	if _, err := os.Stat(filepath.Join(dir, "host_id")); err == nil {
		t.Fatal("an id was minted and published over a recorded fleet identity")
	}
}

// And a lookup that fails on a machine where Supgang exists waits for the
// answer another process here got, rather than minting against it.
func TestAFailedLookupWaitsForTheIdentityAnotherProcessRecorded(t *testing.T) {
	const fleet = "b7c1e0f3a2d4956871bc0e5d4f3a291836cbd75e40a1f2836495d0a7c3e18b26"
	dir := t.TempDir()
	// Supgang is installed and fails for THIS process, and records the
	// answer another process here got while it runs: the race, made
	// deterministic. The script's own write stands in for the sibling.
	old := supgang.Command
	supgang.Command = filepath.Join(dir, "supgang")
	t.Cleanup(func() { supgang.Command = old })
	script := "#!/bin/sh\nprintf '%s' '" + fleet + "' > " +
		filepath.Join(dir, supgang.NodeIDFile) + "\nexit 3\n"
	if err := os.WriteFile(supgang.Command, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	if got := resolveHostID(dir); got != fleet {
		t.Fatalf("a bridge whose own lookup failed answered %q while this machine had just been "+
			"recorded as %q: it mints an id that nothing else here agrees with", got, fleet)
	}
}

// A lookup that succeeds does not overrule an identity another process
// here already published.
//
// Round forty closed one direction of this: a bridge whose own lookup
// failed now waits for the answer a sibling got before minting anything.
// The other direction was left open. A bridge whose lookup fails FAST
// mints and publishes an id while a sibling's lookup is still running,
// and that sibling then answers with the Supgang id for the rest of its
// life: one computer, two identities, and a hub that reads them as two
// machines, which is the split every part of this exists to prevent.
// The minted file is the arbiter for this boot because it is published
// exclusively; the fleet id is adopted at the next start, where nothing
// is racing. Round forty-five of the pre-release review.
func TestASuccessfulLookupDoesNotOverruleAPublishedIdentity(t *testing.T) {
	const fleet = "c3f2a1b0d9e8776655443322110099aabbccddeeff00112233445566778899aa"
	dir := t.TempDir()
	// Supgang answers, and a sibling publishes a minted id while it does:
	// the race, made deterministic by the script that stands in for it.
	old := supgang.Command
	supgang.Command = filepath.Join(dir, "supgang")
	t.Cleanup(func() { supgang.Command = old })
	script := "#!/bin/sh\nprintf '%s' 'minted-by-a-sibling' > " + filepath.Join(dir, "host_id") +
		"\nprintf '%s' '{\"schema\":\"supgang.status/v4\",\"status\":\"ok\"," +
		"\"name\":\"MacSolis\",\"node_id\":\"" + fleet + "\"}'\n"
	if err := os.WriteFile(supgang.Command, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	if got := resolveHostID(dir); got != "minted-by-a-sibling" {
		t.Fatalf("this bridge answers %q while the one that got there first published "+
			"%q: the machine has two identities for as long as both run", got, "minted-by-a-sibling")
	}
	// And the fleet id is remembered, so the next start adopts it and the
	// daemon's rename moves the rows that were registered under the other.
	if got := supgang.RememberedNodeID(dir); got != fleet {
		t.Fatalf("the fleet identity was not remembered (%q), so the next start will not "+
			"adopt it either", got)
	}
}

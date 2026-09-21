package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	// AND A THIRD BRIDGE STARTING NOW READS THE SAME ANSWER. Recording
	// the fleet id as this machine's identity, rather than as pending,
	// is what round fifty found: identityOnDisk reads that file first,
	// so the next process to start answered with the fleet id while the
	// two already running answered with the minted one. One machine,
	// two names, and a hub that reads its agents as two computers.
	if got := resolveHostID(dir); got != "minted-by-a-sibling" {
		t.Fatalf("a bridge starting now answers %q while the two already running answer "+
			"%q: the machine is split three ways", got, "minted-by-a-sibling")
	}
	// The fleet id is recorded as PENDING, so nothing serving changes and
	// the next daemon start adopts it (and renames the rows with it).
	if got := supgang.RememberedNodeID(dir); got != "" {
		t.Fatalf("the fleet identity was taken up mid-boot (%q): that is the split above", got)
	}
	if got := supgang.PromotePendingNodeID(dir); got != fleet {
		t.Fatalf("promoting at the next start gave %q, want the fleet identity %q: without "+
			"it this machine never joins the fleet at all", got, fleet)
	}
	if got := supgang.RememberedNodeID(dir); got != fleet {
		t.Fatalf("after promotion this machine is remembered as %q", got)
	}
}

// Two bridges starting together get one identity even when they take
// DIFFERENT branches to it.
//
// Exclusivity is only exclusivity when everybody competes for the same
// name, and this competed for two: the Supgang branch wrote
// supgang_node_id and the minting branch exclusively created host_id, so
// both could look, see nothing, and then both succeed. One computer, two
// identities, held for the life of both processes; host-scoped claims
// then miss real conflicts between its own agents and the bridge cannot
// wake the ones carrying the other name. The tests before this one
// serialised the two branches, so they never met. Round fifty-one of the
// pre-release review.
func TestBridgesTakingDifferentBranchesStillAgreeOnOneIdentity(t *testing.T) {
	const fleet = "d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3"
	dir := t.TempDir()
	// Supgang answers, slowly enough that the minting branch is running
	// at the same time: the script sleeps before printing.
	old := supgang.Command
	supgang.Command = filepath.Join(dir, "supgang")
	t.Cleanup(func() { supgang.Command = old })
	// A compiled stand-in rather than a script: this repository ships no
	// shell, not even into a temporary directory.
	src := filepath.Join(t.TempDir(), "supgang.go")
	prog := "package main\n\nimport (\n\t\"fmt\"\n\t\"time\"\n)\n\nfunc main() {\n" +
		"\ttime.Sleep(150 * time.Millisecond)\n" +
		"\tfmt.Printf(`{\"schema\":\"supgang.status/v4\",\"status\":\"ok\",\"name\":\"MacSolis\"," +
		"\"node_id\":\"" + fleet + "\"}`)\n}\n"
	if err := os.WriteFile(src, []byte(prog), 0o600); err != nil {
		t.Fatal(err)
	}
	build := exec.Command("go", "build", "-o", supgang.Command, src) // #nosec G204 -- paths this test created
	if out, err := build.CombinedOutput(); err != nil {
		t.Skipf("cannot build the stand-in Supgang here: %v\n%s", err, out)
	}

	// One bridge asks Supgang; the other, with no Supgang of its own,
	// mints. They run at the same time against one directory.
	answers := make(chan string, 2)
	go func() { answers <- resolveHostID(dir) }()
	go func() { answers <- loadOrCreateHostID(dir) }()
	a, b := <-answers, <-answers
	if a != b {
		t.Fatalf("two bridges on one machine answer %q and %q: the hub reads them as two "+
			"computers, their claims stop colliding, and the bridge cannot wake whichever "+
			"carries the name it does not know", a, b)
	}
	if a == "" {
		t.Fatal("neither bridge published an identity at all")
	}

	// AND BOTH COMPETED FOR THE SAME NAME, which is what makes the
	// agreement above structural rather than lucky. The race the
	// concurrent run can hit needs one process to pass its check before
	// the other publishes, and the ordering that produces it is not
	// something a test can force from outside; what a test CAN state is
	// that there is one publication point. Before this there were two,
	// and the Supgang branch never wrote host_id at all.
	published, err := os.ReadFile(filepath.Join(dir, "host_id"))
	if err != nil {
		t.Fatalf("no published identity on disk: %v", err)
	}
	if got := strings.TrimSpace(string(published)); got != a {
		t.Fatalf("the bridges answer %q while the published file holds %q: they are not "+
			"competing for one name, so two of them can both win", a, got)
	}
}

// A Supgang answer is published where a minted one would be, so the two
// branches cannot both succeed.
//
// This is the deterministic half of the case above: whichever branch
// answers, host_id is where it lands. Round fifty-one of the pre-release
// review found that the Supgang branch wrote a different file, so both
// processes could look, see nothing, and each keep its own id.
func TestASupgangAnswerIsPublishedWhereAMintedOneWouldBe(t *testing.T) {
	const fleet = "e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4"
	dir := t.TempDir()
	old := supgang.Command
	supgang.Command = filepath.Join(dir, "supgang")
	t.Cleanup(func() { supgang.Command = old })
	src := filepath.Join(t.TempDir(), "supgang.go")
	prog := "package main\n\nimport \"fmt\"\n\nfunc main() {\n" +
		"\tfmt.Print(`{\"schema\":\"supgang.status/v4\",\"status\":\"ok\"," +
		"\"name\":\"MacSolis\",\"node_id\":\"" + fleet + "\"}`)\n}\n"
	if err := os.WriteFile(src, []byte(prog), 0o600); err != nil {
		t.Fatal(err)
	}
	build := exec.Command("go", "build", "-o", supgang.Command, src) // #nosec G204 -- paths this test created
	if out, err := build.CombinedOutput(); err != nil {
		t.Skipf("cannot build the stand-in Supgang here: %v\n%s", err, out)
	}

	if got := resolveHostID(dir); got != fleet {
		t.Fatalf("a clean start answered %q, want the fleet identity", got)
	}
	published, err := os.ReadFile(filepath.Join(dir, "host_id"))
	if err != nil {
		t.Fatalf("the Supgang answer was not published where a minted one is (%v): the two "+
			"branches write different files, so both can win and the machine splits", err)
	}
	if got := strings.TrimSpace(string(published)); got != fleet {
		t.Fatalf("the published identity is %q, want the fleet one", got)
	}
}

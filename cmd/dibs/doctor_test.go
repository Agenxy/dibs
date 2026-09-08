package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Problem-tier checks are the machine-readable half of doctor. The prose
// already says these states are broken; returning success makes a monitoring
// script disagree with the diagnosis it just printed.
func TestDoctorFailsWhenTheInstallCannotBeChecked(t *testing.T) {
	t.Run("no local secret", func(t *testing.T) {
		t.Setenv("DIBS_DIR", t.TempDir())
		if err := doctor(nil); err == nil {
			t.Fatal("doctor reported a missing local secret but returned success")
		}
	})

	t.Run("daemon unreachable", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "local.secret"), []byte("secret\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("DIBS_DIR", dir)
		// Port 1 on loopback is reserved and used elsewhere in this package as
		// the deterministic "nothing is listening" endpoint.
		t.Setenv("DIBS_ADDR", "127.0.0.1:1")
		if err := doctor(nil); err == nil {
			t.Fatal("doctor reported an unreachable daemon but returned success")
		}
	})

	t.Run("daemon rejects the local secret", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "local.secret"), []byte("stale-secret\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		}))
		defer daemon.Close()
		t.Setenv("DIBS_DIR", dir)
		t.Setenv("DIBS_ADDR", strings.TrimPrefix(daemon.URL, "http://"))
		if err := doctor(nil); err == nil {
			t.Fatal("doctor reported a rejected local secret but returned success")
		}
	})
}

func TestDoctorStatusFollowsTheProblemTier(t *testing.T) {
	tests := []struct {
		name               string
		problems, warnings int
		wantFailure        bool
	}{
		{name: "healthy", problems: 0, warnings: 0},
		{name: "warnings only", problems: 0, warnings: 3},
		{name: "one problem", problems: 1, warnings: 0, wantFailure: true},
		{name: "problems and warnings", problems: 2, warnings: 4, wantFailure: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := doctorResult(tt.problems, tt.warnings)
			if (err != nil) != tt.wantFailure {
				t.Fatalf("doctorResult(%d, %d) error = %v, want failure %v",
					tt.problems, tt.warnings, err, tt.wantFailure)
			}
		})
	}
}

// Doctor flagged the operator's real, working ~/.codex/config.toml as having a
// STALE secret, purely because it was run against a second daemon. The fix it
// offered ("run `dibs mcp-config` and re-copy the block") would have
// repointed a working global setup at whichever scratch daemon happened to be
// running. Anyone with a per-project daemon alongside their usual one hits
// this, and the advice actively breaks them.
//
// A config for another daemon is not a stale config, and the two must not share
// a diagnosis.
func TestAConfigForAnotherDaemonIsNotStale(t *testing.T) {
	codex := `
[mcp_servers.dibs]
url = "http://127.0.0.1:4777/mcp"
http_headers = { "X-Dibs-Local" = "` + strings.Repeat("a", 64) + `" }
`
	if targetsDaemon(codex, "127.0.0.1:4995") {
		t.Fatal("a config naming :4777 does not target the daemon on :4995")
	}
	if !targetsDaemon(codex, "127.0.0.1:4777") {
		t.Fatal("and it plainly does target the one it names")
	}
}

// The message has to name the address the reader needs. These configs hold many
// servers, and the first version of it listed five Google and OpenAI endpoints
// before the Dibs one. Anchoring on the secret is not enough either: a config
// can hold several 64-hex strings, which is exactly the case this branch is for.
func TestTheDiagnosisNamesOnlyTheDibsEndpoint(t *testing.T) {
	mixed := `
[mcp_servers.bigquery]
url = "https://bigquery.googleapis.com/mcp"
token = "` + strings.Repeat("b", 64) + `"

[mcp_servers.dibs]
url = "http://127.0.0.1:4777/mcp"
http_headers = { "X-Dibs-Local" = "` + strings.Repeat("a", 64) + `" }

[mcp_servers.other]
url = "https://developers.openai.com/mcp"
`
	got := dibsTargets(mixed)
	if len(got) != 1 || got[0] != "http://127.0.0.1:4777/mcp" {
		t.Fatalf("only the Dibs endpoint belongs in the hint, got %v", got)
	}
}

// A config with no URL takes its address from the environment: the stdio
// bridge, and several harnesses. Guessing "different daemon" there would trade
// one false alarm for another.
func TestAConfigWithNoURLIsAssumedToBeForThisDaemon(t *testing.T) {
	const stdio = `{"mcpServers":{"dibs":{"command":"dibs","args":["mcp-stdio"]}}}`
	if !targetsDaemon(stdio, "127.0.0.1:4777") {
		t.Fatal("no URL means no evidence of a different daemon")
	}
	if n := len(dibsTargets(stdio)); n != 0 {
		t.Fatalf("nothing to name, got %d", n)
	}
}

// chain builds a small valid ledger, then lets a test damage it.
func chain(t *testing.T) (path string, lines []string) {
	const records = 5 // every caller wants the same fixture
	t.Helper()
	prev := ""
	for i := 1; i <= records; i++ {
		line := fmt.Sprintf(`{"s":%d,"t":"2026-07-28T00:00:00Z","n":"node","e":"noop","prev":%q,"op":{"kind":"noop"}}`,
			i, prev)
		sum := sha256.Sum256([]byte(line))
		prev = hex.EncodeToString(sum[:])
		lines = append(lines, line)
	}
	path = filepath.Join(t.TempDir(), "ledger.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path, lines
}

// A torn tail is NOT damage. The ledger is append-only and fsynced, so a crash
// between write and fsync leaves a partial final record: for an op that was
// never acknowledged to its caller. Ledger.Replay truncates it and carries on.
//
// verify read with a bufio.Scanner, which hands back that partial chunk as
// though it were a complete line, so all it could say was "line N: bad JSON"
// under the heading INTEGRITY FAILURE: while the daemon replayed the same
// file, started, and served it. Two tools, one file, opposite verdicts, on the
// exact surface an operator checks after a crash.
func TestATornFinalRecordIsNotAnIntegrityFailure(t *testing.T) {
	path, lines := chain(t)
	whole, err := verifyChain(path)
	if err != nil || whole.Lines != 5 || whole.Torn {
		t.Fatalf("precondition: want 5 clean lines, got %+v / %v", whole, err)
	}

	// A crash mid-append: a sixth record, cut short, with no trailing newline.
	sixth := lines[4][:len(lines[4])/2]
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"+sixth), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := verifyChain(path)
	if err != nil {
		t.Fatalf("a torn tail must not be reported as damage: %v", err)
	}
	if !got.Torn {
		t.Fatal("it must still SAY the final record is incomplete")
	}
	if got.Lines != 5 {
		t.Fatalf("the complete records still count, got %d", got.Lines)
	}
	// And the head must match the intact prefix, so verify and the daemon agree
	// on the same file: that disagreement was the bug.
	if got.Head != whole.Head {
		t.Fatalf("head diverged from the intact prefix: %s vs %s", got.Head, whole.Head)
	}
}

// Damage in the middle is still damage, and the message must name BOTH records:
// the mismatch is between one record's prev and the hash of the one before it,
// so pointing only at the later one sends the reader to a line that is usually
// intact.
func TestRealDamageIsStillReportedAndNamesBothRecords(t *testing.T) {
	path, lines := chain(t)
	// Alter record 3 in place, keeping it valid JSON and the same length class.
	lines[2] = strings.Replace(lines[2], `"e":"noop"`, `"e":"noOp"`, 1)
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := verifyChain(path)
	if err == nil {
		t.Fatal("an altered record must break the chain")
	}
	if res.Torn {
		t.Fatal("this is corruption, not a torn write")
	}
	for _, want := range []string{"serial 3", "serial 4", "earlier"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the message must mention %q so the reader knows where to look; got: %v", want, err)
		}
	}
}

// A bad record in the MIDDLE is corruption even though it fails the same way a
// torn tail does: the difference is only that the file continues past it.
func TestUnparseableMidFileIsNotMistakenForATornTail(t *testing.T) {
	path, lines := chain(t)
	lines[2] = `{"s":3,"prev":` // valid-looking start, cut short, but not last
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := verifyChain(path)
	if err == nil || res.Torn {
		t.Fatalf("a broken record with more file after it is damage, got %+v / %v", res, err)
	}
}

// A data directory that joins ANOTHER machine's board holds a credential and
// nothing else: the ledger is on the hub.
//
// Reported as "ledger does not verify ... do NOT delete it. Copy it somewhere
// safe and open an issue", which is a data-loss emergency raised against a
// completely healthy join, and raised at the operator least equipped to know
// it is spurious. Found by following `dibs mcp-config --board` end to end.
func TestDoctorDoesNotCallAJoinedBoardsMissingLedgerCorruption(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "local.secret"),
		[]byte(strings.Repeat("a", 64)), 0o600); err != nil {
		t.Fatal(err)
	}

	var good []string
	var bads []string
	ok := func(msg string) { good = append(good, msg) }
	fail := func(msg, _ string) { bads = append(bads, msg) }
	checkLedgerAndBoard(dir, ok, fail, func(msg, _ string) {})

	for _, b := range bads {
		if strings.Contains(b, "ledger does not verify") {
			t.Errorf("a joined board is reported as a corrupt ledger: %q", b)
		}
	}
	if !strings.Contains(strings.Join(good, "\n"), "joined board") {
		t.Errorf("nothing told the operator where the ledger actually is: ok=%v bad=%v", good, bads)
	}

	// And a real board directory must still be verified: node_id is what a
	// daemon writes at first boot, so its presence means this IS a board.
	local := t.TempDir()
	if err := os.WriteFile(filepath.Join(local, "local.secret"),
		[]byte(strings.Repeat("a", 64)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(local, "node_id"), []byte("abc123"), 0o600); err != nil {
		t.Fatal(err)
	}
	bads = nil
	checkLedgerAndBoard(local, func(string) {}, fail, func(msg, _ string) {})
	if len(bads) == 0 {
		t.Error("a board directory with no ledger passed silently: the check has been " +
			"disabled rather than scoped")
	}

	// The dangerous case, which the two above cannot reach: a LOCAL board that
	// has lost its node_id but still holds a ledger. Keyed on node_id alone,
	// that reads as a join and skips verification entirely, so the one
	// directory most in need of checking is reported healthy. Raised by the
	// pre-release review against the first version of this fix.
	damaged := t.TempDir()
	if err := os.WriteFile(filepath.Join(damaged, "local.secret"),
		[]byte(strings.Repeat("a", 64)), 0o600); err != nil {
		t.Fatal(err)
	}
	// Genuinely broken: the second record's prev does not match the hash of the
	// first. A file of arbitrary JSON is NOT damaged, because unknown fields
	// unmarshal cleanly and an empty prev is what a first record has: the first
	// version of this fixture verified as a valid one-line chain and failed the
	// probe rather than the code.
	if err := os.WriteFile(filepath.Join(damaged, "ledger.jsonl"),
		[]byte("{\"s\":1,\"prev\":\"\"}\n{\"s\":2,\"prev\":\"deadbeef\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	good, bads = nil, nil
	checkLedgerAndBoard(damaged, ok, fail, func(msg, _ string) {})
	if strings.Contains(strings.Join(good, "\n"), "joined board") {
		t.Errorf("a board holding a ledger with no node_id was called a join, so its "+
			"chain was never verified: ok=%v", good)
	}
	if len(bads) == 0 {
		t.Error("a damaged ledger passed unverified because node_id was missing")
	}
}

// A join is a credential and NOTHING ELSE, which is not the same as a missing
// node_id and ledger.
//
// A local board that lost both, but still holds the key it encrypts with and
// the blobs it wrote, was reported as a healthy join. That directory has lost
// its replayable state, which is the one thing this check exists to notice, and
// it was told nothing was wrong. Found by the pre-release review.
func TestADamagedLocalBoardIsNotMistakenForAJoin(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "local.secret"),
		[]byte(strings.Repeat("a", 64)), 0o600); err != nil {
		t.Fatal(err)
	}
	// No node_id, no ledger: but the key it encrypted with is still here.
	if err := os.WriteFile(filepath.Join(dir, "key"), []byte("k"), 0o600); err != nil {
		t.Fatal(err)
	}
	if isJoinedBoard(dir) {
		t.Error("a board that lost its node_id and its ledger but kept its key was " +
			"called a healthy join, so nothing reported the loss")
	}

	// Every daemon-owned artifact, not just the one the first version happened
	// to name. A joining client holds the board's public certificate; it never
	// holds the private key or the admin hash.
	for _, own := range []string{
		"tls-key.pem", "admin.hash", "blobs", "coordinator.claim", "out",
		// The ordinary output of `dibs configure`: a configured local board that
		// lost everything else is the one whose loss most deserves reporting.
		"dibs.toml",
	} {
		d := t.TempDir()
		if err := os.WriteFile(filepath.Join(d, "local.secret"),
			[]byte(strings.Repeat("a", 64)), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, own), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if isJoinedBoard(d) {
			t.Errorf("a directory holding %s is a board that has lost its ledger, not a "+
				"join, and reporting it healthy hides exactly that loss", own)
		}
	}

	// And the public certificate alone does NOT make it a board: a joining
	// machine records that legitimately, via `dibs trust`.
	joined := t.TempDir()
	for _, f := range []string{"local.secret", "tls-cert.pem", "trusted-certs.pem", "harness-nonces.json"} {
		if err := os.WriteFile(filepath.Join(joined, f), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if !isJoinedBoard(joined) {
		t.Error("a joined directory holding a credential and a trusted certificate was " +
			"not recognised as a join")
	}

	// The real join case still is one.
	clean := t.TempDir()
	if err := os.WriteFile(filepath.Join(clean, "local.secret"),
		[]byte(strings.Repeat("a", 64)), 0o600); err != nil {
		t.Fatal(err)
	}
	if !isJoinedBoard(clean) {
		t.Error("a directory holding only a credential is not recognised as a join")
	}
}

// With the daemon down, doctor must still reach the LOCAL checks.
//
// A damaged ledger usually presents as an unreachable daemon: dibd refuses to
// replay it and never listens. The old implementation returned as soon as the
// connection failed, so the one check that says why, and tells the operator not
// to delete the file, never ran in the case it exists for.
//
// The existing unreachable-daemon test asserts only that doctor returns an
// error, and the early-return version did that too: restoring the exact
// regression left it green. The pre-release review made that point, so this
// asserts the call chain instead of the exit status, against a ledger that is
// genuinely broken rather than merely unparsable.
func TestDoctorStillChecksTheLedgerWhenTheDaemonIsDown(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "local.secret"), []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// node_id present: without it this reads as a JOINED board, which skips
	// ledger verification entirely and would pass while proving nothing.
	if err := os.WriteFile(filepath.Join(dir, "node_id"), []byte("test-node\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A real broken chain. Arbitrary JSON unmarshals cleanly and verifies as an
	// empty ledger, which is how an earlier fixture of mine tested nothing: the
	// second record has to claim a predecessor hash that the first does not have.
	broken := `{"s":1,"prev":"","k":"register"}` + "\n" + `{"s":2,"prev":"deadbeef","k":"register"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "ledger.jsonl"), []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DIBS_DIR", dir)
	// Port 1 on loopback: nothing listens there, deterministically.
	t.Setenv("DIBS_ADDR", "127.0.0.1:1")

	out, err := captureStdout(t, func() error { return doctor(nil) })
	if err == nil {
		t.Fatal("doctor reported success with an unreachable daemon and a broken ledger")
	}
	if !strings.Contains(out, "unreachable") {
		t.Fatalf("setup: doctor did not report the daemon unreachable, so this test "+
			"is not exercising the path it was written for:\n%s", out)
	}
	if !strings.Contains(out, "ledger") {
		t.Errorf("doctor stopped at the unreachable daemon and never checked the "+
			"ledger, which is where the reason for an unreachable daemon usually "+
			"is, and which tells the operator NOT to delete the file:\n%s", out)
	}
}

// doctor must talk to the board `dibs configure` wrote, not to the default.
//
// dibd resolves flag -> DIBS_ADDR -> dibs.toml, and the wizard writes the
// operator's answer to the file. doctor built its request from the environment
// alone, so a healthy daemon configured only in dibs.toml was reported
// unreachable and the harness configurations were then checked against a board
// that was never running. CONFIGURATION.md promises doctor reports what is
// actually in effect. Found by the pre-release review.
func TestDoctorUsesTheAddressConfigureWrote(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "local.secret"), []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`))
	}))
	defer daemon.Close()
	hostPort := strings.TrimPrefix(daemon.URL, "http://")

	// The address lives ONLY in the file, which is the case that was broken.
	toml := "addr = \"" + hostPort + "\"\n"
	if err := os.WriteFile(filepath.Join(dir, "dibs.toml"), []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DIBS_DIR", dir)
	t.Setenv("DIBS_ADDR", "")

	out, _ := captureStdout(t, func() error { return doctor(nil) })
	if strings.Contains(out, "unreachable") {
		t.Errorf("doctor called a healthy daemon unreachable because the address was "+
			"in dibs.toml rather than the environment, and then checked every "+
			"harness against the board it guessed instead:\n%s", out)
	}
	if !strings.Contains(out, hostPort) {
		t.Errorf("doctor never names the configured address %s:\n%s", hostPort, out)
	}
}

// Every shipped hook file is judged against the id its own harness uses.
//
// The two layouts are two harnesses. Claude Code reads
// `<plugin>/hooks/hooks.json` and a hook there addresses
// `plugin:<plugin>:<server>`; Codex reads a `hooks.json` at the root of its
// config directory and addresses the server directly. Teaching the scanner to
// read both layouts without teaching it that they spell the address
// differently made `dibs doctor` report the correct shipped Codex hook as
// pointed at a server that does not exist, and prescribe reinstalling the
// plugin, which cannot fix a file that is already right.
//
// AGAINST THE SHIPPED FILES, not a fixture: the defect was a disagreement
// between the scanner and what this repository actually ships, and a fixture
// would only record my idea of that.
func TestEveryShippedHookIsJudgedByItsOwnHarnessConvention(t *testing.T) {
	// FROM THE REPOSITORY ROOT, because scanShippedHooks reads a relative
	// `plugins` directory and a package test runs in its own. Without this it
	// scanned nothing, found nothing, and passed: the first version of this test
	// could not fail, which is the defect it was written to catch, in the test.
	t.Chdir("../..")
	wanted, misaddressed := scanShippedHooks()
	if len(wanted) == 0 {
		t.Fatal("the scanner found no shipped hooks at all, so this check verified " +
			"nothing. It reads a relative `plugins` directory; if that moved, this " +
			"test has been passing over an empty scan")
	}
	if len(misaddressed) > 0 {
		for id, file := range misaddressed {
			t.Errorf("the scanner rejects %s, which names server %q. Either the shipped "+
				"file is wrong or the scanner is, and `dibs doctor` tells the operator to "+
				"reinstall a plugin over it either way", file, id)
		}
	}

	// And the rule itself, both ways, so a scanner that simply accepted
	// everything would not pass this.
	for file, want := range map[string]string{
		filepath.Join("plugins", "claude-code", "hooks", "hooks.json"): wantServer,
		filepath.Join("plugins", "codex", "hooks.json"):                codexServer,
	} {
		if got := serverIDFor(file); got != want {
			t.Errorf("a hook in %s is expected to address %q, want %q", file, got, want)
		}
	}
}

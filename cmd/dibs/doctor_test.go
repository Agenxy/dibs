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
}

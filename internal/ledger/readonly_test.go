package ledger

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// A read-only handle reports a torn tail and does not repair it.
//
// THE DATA LOSS THIS CATCHES. `dibd -check` replays a board to answer whether
// this build could take over from the daemon now running, and `dibs upgrade`
// runs it BEFORE the cutover, which means while the old daemon may still be
// serving and still be mid-write. It used the ordinary Open, so a question
// about the board repaired the board: Replay truncates a torn final line, and
// the torn final line is exactly what a running writer looks like from outside.
//
// Measured against the previous commit with a 17-byte partial record: the
// command reported success and left the ledger at 0 bytes.
//
// Repairing a torn tail is right for the process that OWNS the file. It is
// never right for a process asking a question about somebody else's.
func TestAReadOnlyReplayLeavesATornTailAlone(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ledger.jsonl")
	box, err := LoadOrCreateKey(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}

	// One good record, then a partial one, as an interrupted write leaves it.
	led, err := Open(path, "node", box)
	if err != nil {
		t.Fatal(err)
	}
	st := core.NewState("node", core.DefaultLimits())
	if _, _, err := st.Apply(&core.Op{Kind: core.OpRegister, Name: "a", NewToken: "t"}, tornTestTime()); err != nil {
		t.Fatal(err)
	}
	if err := led.Append(1, tornTestTime(), &core.Op{Kind: core.OpRegister, Name: "a", NewToken: "t"}); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"s":2,"partial`); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	ro, err := OpenReadOnly(path, "node", box)
	if err != nil {
		t.Fatal(err)
	}
	var reported bool
	ro.OnTornTail = func(int, int64) { reported = true }
	fresh := core.NewState("node", core.DefaultLimits())
	if _, err := ro.Replay(fresh); err != nil {
		t.Fatalf("read-only replay failed: %v", err)
	}

	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if after.Size() != before.Size() {
		t.Errorf("a read-only replay changed the file: %d bytes to %d. `dibs upgrade` "+
			"runs this against a directory the previous daemon may still be serving",
			before.Size(), after.Size())
	}
	if !reported {
		t.Error("the torn tail was neither repaired nor reported, which is worse than " +
			"either: the caller cannot tell a clean board from a damaged one")
	}
}

func tornTestTime() time.Time { return time.Unix(1700000000, 0) }

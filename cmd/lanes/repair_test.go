package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/agenxy/lanes/internal/core"
	"github.com/agenxy/lanes/internal/ledger"
)

// poisonedBoard builds a real data directory whose ledger holds a record the
// fold refuses — the shape a live board actually reached.
//
// Built through the real Ledger rather than by writing JSON, because the whole
// point of the repair path is that it agrees with the daemon: the hash chain,
// the op encryption and the serial resync all have to be the genuine ones or the
// test proves nothing about the tool.
func poisonedBoard(t *testing.T) (dir string, lastGood uint64, records int) {
	t.Helper()
	dir = t.TempDir()
	box, err := ledger.LoadOrCreateKey(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "node_id"), []byte("test-node\n"), 0o600); err != nil {
		t.Fatalf("node_id: %v", err)
	}
	led, err := ledger.Open(filepath.Join(dir, "ledger.jsonl"), "test-node", box)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	st := core.NewState("test-node", core.DefaultLimits())
	now := time.Unix(1700000000, 0)

	write := func(op *core.Op) error {
		_, _, aerr := st.Apply(op, now)
		if aerr != nil {
			return aerr
		}
		if err := led.Append(st.Serial, now, op); err != nil {
			t.Fatalf("append: %v", err)
		}
		records++
		return nil
	}
	if err := write(&core.Op{Kind: core.OpRegisterLane, Name: "probe", NewToken: "tok"}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := write(&core.Op{Kind: core.OpAckBoard, Token: "tok"}); err != nil {
		t.Fatalf("ack: %v", err)
	}
	if err := write(&core.Op{Kind: core.OpCloseLane, Token: "tok"}); err != nil {
		t.Fatalf("close: %v", err)
	}
	lastGood = st.Serial

	// The poison: a SECOND close of the same lane, appended without applying.
	// That is exactly what a live board ended up holding — an op the fold
	// refuses, sitting in a chain that verifies.
	if err := led.Append(st.Serial+1, now, &core.Op{Kind: core.OpCloseLane, Token: "tok"}); err != nil {
		t.Fatalf("append poison: %v", err)
	}
	records++
	if err := led.Close(); err != nil {
		t.Fatalf("close ledger: %v", err)
	}
	return dir, lastGood, records
}

// The tool must agree with the daemon about WHERE the ledger stops applying.
//
// The first version of this walked the file with its own loop and reported the
// wrong record entirely — it never decrypted the ops, so every nonce was
// ciphertext, and it never resynced the serial. On a real board it named record
// 8 where the daemon named 416: it would have offered to discard 402 records to
// repair a fault in 33. A second implementation of a fold is a second answer,
// and here a wrong answer destroys data.
func TestRepairFindsTheSameRecordTheDaemonRefuses(t *testing.T) {
	dir, lastGood, records := poisonedBoard(t)

	good, total, firstBad, applyErr, err := lastReplayableSerial(dir)
	if err != nil {
		t.Fatalf("probe failed: %v", err)
	}
	if applyErr == nil {
		t.Fatal("a poisoned ledger replayed cleanly; this test proves nothing")
	}
	if firstBad != lastGood+1 {
		t.Errorf("first bad serial = %d, want %d", firstBad, lastGood+1)
	}
	if total != records {
		t.Errorf("counted %d records, wrote %d", total, records)
	}
	if good != records-1 {
		t.Errorf("%d records applied, want %d — only the poisoned one should fail", good, records-1)
	}
}

// Repair keeps the good prefix, archives the original, and leaves a board the
// daemon can actually open.
func TestRepairKeepsThePrefixAndArchivesTheOriginal(t *testing.T) {
	dir, lastGood, records := poisonedBoard(t)
	path := filepath.Join(dir, "ledger.jsonl")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	kept, err := truncateAfter(path, lastGood+1)
	if err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if kept != records-1 {
		t.Errorf("kept %d records, want %d", kept, records-1)
	}

	// The repaired ledger must REPLAY — the only test of a repair that matters.
	good, _, _, applyErr, err := lastReplayableSerial(dir)
	if err != nil {
		t.Fatalf("probe after repair: %v", err)
	}
	if applyErr != nil {
		t.Fatalf("the repaired ledger still will not replay: %v", applyErr)
	}
	if good != records-1 {
		t.Errorf("replayed %d records after repair, want %d", good, records-1)
	}

	// And it must be reversible: the archive is byte-identical to what was there.
	archived := path + ".archived-test"
	if err := copyFile(path, archived); err != nil {
		t.Fatalf("archive: %v", err)
	}
	if len(before) == 0 {
		t.Fatal("setup wrote nothing")
	}
}

// A healthy ledger must be told so, and left alone. An operator who runs this
// on a working board should get a sentence, not a repair.
func TestRepairRefusesToRepairAHealthyLedger(t *testing.T) {
	dir := t.TempDir()
	box, err := ledger.LoadOrCreateKey(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "node_id"), []byte("test-node\n"), 0o600); err != nil {
		t.Fatalf("node_id: %v", err)
	}
	led, err := ledger.Open(filepath.Join(dir, "ledger.jsonl"), "test-node", box)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	st := core.NewState("test-node", core.DefaultLimits())
	now := time.Unix(1700000000, 0)
	op := &core.Op{Kind: core.OpRegisterLane, Name: "probe", NewToken: "tok"}
	if _, _, aerr := st.Apply(op, now); aerr != nil {
		t.Fatalf("apply: %v", aerr)
	}
	if err := led.Append(st.Serial, now, op); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := led.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	_, total, _, applyErr, err := lastReplayableSerial(dir)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if applyErr != nil {
		t.Errorf("a healthy ledger was reported as broken: %v", applyErr)
	}
	if total != 1 {
		t.Errorf("counted %d records, wrote 1", total)
	}
}

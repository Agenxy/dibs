package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// chainResult is what an integrity check found.
//
// Torn is separate from an error because a torn tail is NOT damage. The ledger
// is append-only and fsynced, so a crash between write and fsync leaves a
// partial final record: the expected artifact, for an op that was never
// acknowledged to its caller. Ledger.Replay truncates it and carries on.
//
// verify used to read with a bufio.Scanner, which hands back that partial chunk
// as though it were a complete line, so the only thing it could say was
// "line N: bad JSON" under the heading INTEGRITY FAILURE. The daemon replayed
// the same file, started, and served. Two tools, one file, opposite verdicts,
// and the alarming one was wrong, on the exact surface an operator checks after
// a crash.
type chainResult struct {
	Lines int    // complete, chained records
	Head  string // hash of the last complete record
	Torn  bool   // the final record is a partial write, not corruption
}

// verifyChain re-hashes every raw ledger line and checks each record's prev
// pointer. Works on ciphertext: no daemon key needed for integrity checks.
//
// Reads with ReadString('\n') rather than a Scanner so it can tell a record cut
// short by EOF from a bad record in the middle of the file, which is the same
// rule Ledger.Replay applies. A complete record always ends in a newline.
func verifyChain(path string) (chainResult, error) {
	// The operator points `lanes verify` at a ledger file: that is the whole
	// command, and it reads as the same user either way. Cleaned so a path with
	// traversal segments resolves before it is opened rather than after.
	f, err := os.Open(filepath.Clean(path)) // #nosec G703 -- operator-supplied by design
	if err != nil {
		return chainResult{}, err
	}
	defer func() { _ = f.Close() }()

	r := bufio.NewReaderSize(f, 1<<20)
	var res chainResult
	prev := ""
	prevSerial := uint64(0)
	for {
		raw, rerr := r.ReadString('\n')
		if rerr != nil && !errors.Is(rerr, io.EOF) {
			return res, rerr
		}
		if errors.Is(rerr, io.EOF) && raw == "" {
			break // clean end of file
		}
		if errors.Is(rerr, io.EOF) {
			// Ran out of file mid-record: a torn write, not corruption.
			res.Torn = true
			break
		}
		line := []byte(raw[:len(raw)-1]) // drop the newline, as Scanner did
		var rec struct {
			S    uint64 `json:"s"`
			Prev string `json:"prev"`
		}
		if err := json.Unmarshal(line, &rec); err != nil {
			return res, fmt.Errorf("line %d: bad JSON: %w", res.Lines+1, err)
		}
		if rec.Prev != prev {
			// Name BOTH records. The mismatch is between this record's `prev`
			// and the hash of the one before it, so the damage is in either,
			// and pointing only at the later one sends the reader to a line
			// that is usually intact.
			return res, fmt.Errorf(
				"chain broken between serial %d and serial %d: serial %d's prev does not "+
					"match the hash of serial %d, so one of those two records was altered "+
					"(most often the earlier one)",
				prevSerial, rec.S, rec.S, prevSerial,
			)
		}
		sum := sha256.Sum256(line)
		prev = hex.EncodeToString(sum[:])
		prevSerial = rec.S
		res.Lines++
		res.Head = prev
	}
	return res, nil
}

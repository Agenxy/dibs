// Package ledger implements the append-only, hash-chained JSONL event log.
// The ledger IS the persistence, the audit history, and the serial authority:
// line position = serial, line order = total order. Each line carries the
// SHA-256 of the previous raw line, making history tamper-evident. Private
// fields (message bodies, agent tokens) are encrypted with the daemon key
// before writing, so ledger readers see ciphertext while `dibs` (same user)
// can decrypt.
package ledger

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// Line is one ledger record. Prev chains to the SHA-256 hex of the previous
// raw line bytes (hashing bytes-as-written needs no canonicalization).
type Line struct {
	S    uint64    `json:"s"`
	T    time.Time `json:"t"`
	N    string    `json:"n"`
	E    string    `json:"e"`
	Prev string    `json:"prev"`
	Op   *core.Op  `json:"op"`
}

// Ledger is a single-writer append log. Not safe for concurrent use: the
// engine's single goroutine is the only writer, by design.
type Ledger struct {
	f        *os.File
	headHash string
	box      *Box
	nodeID   string

	// OnEvents, if set, receives every event regenerated during Replay.
	OnEvents func([]core.Event)

	// OnSerialGap, if set, is told when Replay finds the ledger's serial ahead
	// of the replayed state: a serial the writer allocated but never appended.
	// Survivable (see Replay), but never normal: the daemon logs it so a writer
	// bug leaves a trail instead of being silently smoothed over.
	OnSerialGap func(stateSerial, ledgerSerial uint64)

	// OnTornTail, if set, is told when Replay discards a partial final record,
	// the ordinary artifact of a crash between write and fsync.
	//
	// Dropping it is correct: the daemon died before it could answer the caller,
	// so nobody was ever told that op succeeded. Doing it SILENTLY is not.
	// Replay truncates the file to remove the fragment, which is the one place
	// in this system where reading the ledger writes to it, and the log said
	// only "ledger replayed ops=7": indistinguishable from a board that always
	// had seven. By the standard OnSerialGap already sets ("a silent resync
	// would hide the next writer bug completely"), a torn tail earns a line too,
	// and more so, because this one destroys bytes.
	OnTornTail func(bytes int, atOffset int64)
}

const genesis = ""

// Open opens (creating if needed) the ledger at path.
func Open(path, nodeID string, box *Box) (*Ledger, error) {
	// #nosec G304 -- `path` is the daemon's own ledger inside its data directory,
	// chosen by the operator via -dir/DIBS_DIR. Refusing it means refusing to run.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open ledger: %w", err)
	}
	return &Ledger{f: f, headHash: genesis, box: box, nodeID: nodeID}, nil
}

// Replay folds every ledger line into st, truncating a torn tail if the last
// write was interrupted. Returns the number of ops replayed.
// OnEvents, if set, receives every event regenerated during Replay. The engine
// uses it to rebuild its event ring, so an agent's cursor survives a daemon
// restart. Without it the ring starts empty and the floor jumps to whatever
// serial the daemon happened to restart at: invalidating every cursor in the
// fleet, which is exactly what happened during a day of rebuilds.
func (l *Ledger) Replay(st *core.State) (int, error) {
	if _, err := l.f.Seek(0, io.SeekStart); err != nil {
		return 0, err
	}
	r := bufio.NewReaderSize(l.f, 1<<20)
	var off, validOff int64
	prev := genesis
	n := 0
	for {
		raw, err := r.ReadBytes('\n')
		if len(raw) > 0 && err == nil {
			line := raw[:len(raw)-1]
			var rec Line
			if jsonErr := json.Unmarshal(line, &rec); jsonErr != nil {
				return n, fmt.Errorf("ledger corrupt at offset %d (serial ~%d): %w", off, st.Serial+1, jsonErr)
			}
			if rec.Prev != prev {
				return n, fmt.Errorf("hash chain broken at serial %d: prev %.12q != head %.12q", rec.S, rec.Prev, prev)
			}
			// A line can be valid JSON and still carry no op: a truncation that
			// lands on a brace, a hand-edit, a file from something else. Without
			// this the nil reaches DecryptOp and the daemon panics with a stack
			// trace, which is the one output that tells an operator nothing and
			// hides the careful diagnosis waiting further up the caller.
			if rec.Op == nil {
				return n, fmt.Errorf("ledger corrupt at offset %d (serial %d): record carries no op", off, rec.S)
			}
			if decErr := l.box.DecryptOp(rec.Op); decErr != nil {
				return n, fmt.Errorf("decrypt serial %d: %w", rec.S, decErr)
			}
			_, replayEvents, applyErr := st.Apply(rec.Op, rec.T)
			if l.OnEvents != nil && len(replayEvents) > 0 {
				l.OnEvents(replayEvents)
			}
			if applyErr != nil {
				return n, fmt.Errorf("replay apply serial %d: %w", rec.S, applyErr)
			}
			// The hash chain above is the integrity guarantee, not this number.
			//
			// A serial that runs AHEAD of the replayed state means the writer
			// allocated a serial it never appended: a hole. That is a writer
			// bug, and it happened: a real board reached serial 566 with 565
			// lines, missing 447, chain fully intact. Treating it as fatal made
			// the daemon refuse to start at all, so a bug that lost nothing
			// permanently destroyed access to everything. Refusing to open a
			// ledger whose chain verifies is the worse failure by far.
			//
			// So resync and carry on, but say so: a gap is worth diagnosing even
			// though it is survivable, and a silent resync would hide the next
			// writer bug completely.
			//
			// BACKWARD is still fatal. rec.S < st.Serial means replay produced
			// MORE transitions than the writer recorded: the state machine and
			// the ledger genuinely disagree about what an op does, and every
			// serial from here on would be wrong.
			if rec.S < st.Serial {
				return n, fmt.Errorf("serial went backwards on replay: state %d, ledger %d "+
					"(the state machine produced transitions the ledger does not record)", st.Serial, rec.S)
			}
			if st.Serial != rec.S {
				if l.OnSerialGap != nil {
					l.OnSerialGap(st.Serial, rec.S)
				}
				st.Serial = rec.S
			}
			sum := sha256.Sum256(line)
			prev = hex.EncodeToString(sum[:])
			off += int64(len(raw))
			validOff = off
			n++
			continue
		}
		if errors.Is(err, io.EOF) {
			// A torn final line (no trailing newline / partial JSON) is the
			// expected crash artifact: truncate it and continue.
			if len(raw) > 0 {
				if err := l.f.Truncate(validOff); err != nil {
					return n, fmt.Errorf("truncate torn tail: %w", err)
				}
				if l.OnTornTail != nil {
					l.OnTornTail(len(raw), validOff)
				}
			}
			break
		}
		if err != nil {
			return n, err
		}
	}
	l.headHash = prev
	if _, err := l.f.Seek(0, io.SeekEnd); err != nil {
		return n, err
	}
	return n, nil
}

// Append writes one op as the record for serial s, fsyncing before return.
// Must be called after the op was applied (serial already assigned).
func (l *Ledger) Append(serial uint64, ts time.Time, op *core.Op) error {
	enc := *op // shallow copy; encryption replaces sensitive fields
	if err := l.box.EncryptOp(&enc); err != nil {
		return err
	}
	rec := Line{S: serial, T: ts, N: l.nodeID, E: op.Kind, Prev: l.headHash, Op: &enc}
	line, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	if _, err := l.f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("ledger append: %w", err)
	}
	if err := l.f.Sync(); err != nil {
		return fmt.Errorf("ledger fsync: %w", err)
	}
	sum := sha256.Sum256(line)
	l.headHash = hex.EncodeToString(sum[:])
	return nil
}

// HeadHash returns the current chain head (anchor this externally).
func (l *Ledger) HeadHash() string { return l.headHash }

// Close closes the underlying file.
func (l *Ledger) Close() error { return l.f.Close() }

// Verify walks a ledger file and checks the hash chain without needing the
// daemon key (hashes cover ciphertext). Returns lines checked and head hash.
func Verify(path string) (int, string, error) {
	// #nosec G304 -- operator-supplied ledger path; `dibs verify` exists precisely
	// to be pointed at a file.
	f, err := os.Open(path)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<24)
	prev := genesis
	n := 0
	for sc.Scan() {
		line := sc.Bytes()
		var rec struct {
			S    uint64 `json:"s"`
			Prev string `json:"prev"`
		}
		if err := json.Unmarshal(line, &rec); err != nil {
			return n, prev, fmt.Errorf("line %d: bad JSON: %w", n+1, err)
		}
		if rec.Prev != prev {
			return n, prev, fmt.Errorf("chain broken at serial %d", rec.S)
		}
		sum := sha256.Sum256(line)
		prev = hex.EncodeToString(sum[:])
		n++
	}
	return n, prev, sc.Err()
}

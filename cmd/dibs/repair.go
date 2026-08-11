package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/ledger"
	"github.com/agenxy/dibs/internal/ui"
)

// repairLedger recovers a board whose ledger contains a record the fold refuses.
//
// State IS the ledger here, so a line that will not apply means every line after
// it describes a board that can no longer be reconstructed, and the daemon
// correctly refuses to start. Correctly: and, until now, terminally: the
// operator got one wrapped Go error and no next step, on the one failure where
// the entire product is unavailable until it is resolved. A real board hit
// exactly this (`replay apply serial 416: E_LANE_CLOSED`) and there was nothing
// to type.
//
// The recovery is the only one the hash chain permits. Each record hashes its
// predecessor, so a bad line in the middle cannot be excised: everything after
// it is chained to it. What CAN be kept is the prefix that still applies, which
// is what the torn-tail path already does for a crash mid-write. This is the
// same operation, made explicit and deliberate because the cause is different:
// a torn tail is half a record nobody was promised, while this discards whole
// records that were acknowledged.
//
// So it never runs by itself and never runs silently. It replays into a scratch
// state to find the last serial that applies, prints exactly what it would
// discard, and requires the operator to say yes. The original is archived beside
// the ledger rather than overwritten: the same `.archived-<stamp>` convention
// the data directory already uses, so this is reversible with a `mv`.
func repairLedger(args []string) error {
	yes := false
	for _, a := range args {
		if a == "--yes" || a == "-y" {
			yes = true
		} else {
			return fmt.Errorf("`dibs admin repair-ledger` takes only --yes, got %q", a)
		}
	}
	path := ledgerPath()
	dir := filepath.Dir(path)

	good, total, badSerial, applyErr, err := lastReplayableSerial(dir)
	if err != nil {
		return err
	}
	if applyErr == nil {
		fmt.Printf("%s\n", ui.Good("this ledger replays cleanly: nothing to repair"))
		fmt.Printf("  %d records, all of them apply\n", total)
		return nil
	}

	discarded := total - good
	fmt.Println(ui.Bad("this board cannot be rebuilt from its own ledger"))
	fmt.Printf("  record %d will not apply: %v\n", badSerial, applyErr)
	fmt.Printf("  %d of %d records still apply; %d would be discarded\n\n", good, total, discarded)
	if err := summariseDiscarded(path, badSerial); err != nil {
		return err
	}
	fmt.Printf("\n  The original is archived beside the ledger, so this is reversible.\n")
	if !yes {
		fmt.Printf("\n%s\n", ui.Dim("nothing has been changed: rerun with --yes to do it"))
		return nil
	}

	stamp := time.Now().Format("20060102-150405")
	archived := path + ".archived-" + stamp
	if err := copyFile(path, archived); err != nil {
		return fmt.Errorf("archiving the original first: %w", err)
	}
	kept, err := truncateAfter(path, badSerial)
	if err != nil {
		return fmt.Errorf("the original is safe at %s: %w", archived, err)
	}
	fmt.Printf("%s\n", ui.Good(fmt.Sprintf("kept %d records; archived the original", kept)))
	fmt.Printf("  original: %s\n", archived)
	fmt.Printf("  start the daemon again: dibd\n")
	return nil
}

// lastReplayableSerial replays the ledger EXACTLY as the daemon does, and
// reports where it stopped.
//
// Through ledger.Replay itself rather than a hand-rolled fold, and that is not
// tidiness: a second implementation gets a different answer, which here means
// discarding the wrong records. The first version of this walked the file with
// its own loop and reported record 8 failing on E_BAD_NONCE where the daemon
// failed at 416: it never decrypted the ops (so every nonce was ciphertext) and
// never resynced st.Serial to the record's. It would have offered to throw away
// 402 records to fix a fault in 33.
//
// The bad serial is st.Serial+1 rather than a parsed error string: Replay syncs
// the state's serial to each record it applies, so after it stops, the state is
// standing on the last record that worked.
func lastReplayableSerial(dir string) (good, total int, firstBad uint64, applyErr error, err error) {
	total, err = countRecords(filepath.Join(dir, "ledger.jsonl"))
	if err != nil {
		return 0, 0, 0, nil, err
	}
	// #nosec G304 -- the board's own data directory, named by -dir/DIBS_DIR.
	nodeID, err := os.ReadFile(filepath.Join(dir, "node_id"))
	if err != nil {
		return 0, 0, 0, nil, fmt.Errorf("cannot read the board's node id: %w", err)
	}
	box, err := ledger.LoadOrCreateKey(filepath.Join(dir, "key"))
	if err != nil {
		return 0, 0, 0, nil, fmt.Errorf("cannot read the board's ledger key: %w", err)
	}
	led, err := ledger.Open(filepath.Join(dir, "ledger.jsonl"), strings.TrimSpace(string(nodeID)), box)
	if err != nil {
		return 0, 0, 0, nil, err
	}
	defer func() { _ = led.Close() }()

	st := core.NewState(strings.TrimSpace(string(nodeID)), core.DefaultLimits())
	good, applyErr = led.Replay(st)
	if applyErr != nil {
		firstBad = st.Serial + 1
	}
	return good, total, firstBad, applyErr, nil
}

// countRecords is the denominator: how many lines the file actually holds.
func countRecords(path string) (int, error) {
	// #nosec G304 -- the operator's own data directory.
	f, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("cannot read the ledger at %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	n := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<24)
	for sc.Scan() {
		if len(sc.Bytes()) > 0 {
			n++
		}
	}
	return n, sc.Err()
}

// summariseDiscarded says WHAT would be lost, by kind. A count alone cannot tell
// an operator whether they are about to drop thirty probe registrations or a
// day of coordination.
func summariseDiscarded(path string, from uint64) error {
	// #nosec G304 -- as above.
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	counts := map[string]int{}
	var order []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<24)
	for sc.Scan() {
		var rec struct {
			S  uint64 `json:"s"`
			Op struct {
				Kind string `json:"kind"`
			} `json:"op"`
		}
		if json.Unmarshal(sc.Bytes(), &rec) != nil || rec.S < from {
			continue
		}
		if counts[rec.Op.Kind] == 0 {
			order = append(order, rec.Op.Kind)
		}
		counts[rec.Op.Kind]++
	}
	fmt.Println("  what would be discarded:")
	for _, k := range order {
		fmt.Printf("    %-22s %d\n", k, counts[k])
	}
	return sc.Err()
}

// truncateAfter rewrites the ledger with every record before the bad serial.
func truncateAfter(path string, from uint64) (int, error) {
	// #nosec G304 -- as above.
	src, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer func() { _ = src.Close() }()

	tmp := path + ".repair-tmp"
	// #nosec G304 -- written next to the ledger, in the operator's own dir.
	dst, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, err
	}
	kept := 0
	w := bufio.NewWriter(dst)
	sc := bufio.NewScanner(src)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<24)
	for sc.Scan() {
		var rec struct {
			S uint64 `json:"s"`
		}
		if json.Unmarshal(sc.Bytes(), &rec) != nil || rec.S >= from {
			continue
		}
		if _, werr := w.Write(append(sc.Bytes(), '\n')); werr != nil {
			_ = dst.Close()
			return 0, werr
		}
		kept++
	}
	if err := sc.Err(); err != nil {
		_ = dst.Close()
		return 0, err
	}
	if err := w.Flush(); err != nil {
		_ = dst.Close()
		return 0, err
	}
	// fsync before the rename, for the same reason the ledger does it on every
	// append: a rename that lands before its contents leaves a board that
	// replays to something nobody chose.
	if err := dst.Sync(); err != nil {
		_ = dst.Close()
		return 0, err
	}
	if err := dst.Close(); err != nil {
		return 0, err
	}
	return kept, os.Rename(tmp, path)
}

// copyFile archives the ledger beside itself before anything is rewritten.
// Both paths are derived from the operator's own data directory, never from
// anything a caller supplied.
func copyFile(from, to string) error {
	// #nosec G304 -- the operator's own data directory.
	b, err := os.ReadFile(from)
	if err != nil {
		return err
	}
	// #nosec G703 -- `to` is `from` plus an archive suffix this function builds.
	return os.WriteFile(to, b, 0o600)
}

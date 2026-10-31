// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package ledger

import (
	"bufio"
	"bytes"
	"encoding/gob"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// A snapshot is the board's state at one ledger serial, so a start replays
// only the ledger lines after it instead of every op the board ever applied.
//
// The ledger is the record and state is a fold over it; without a snapshot
// every start re-ran the whole history to rebuild a board that had already
// pruned it. On 2026-10-12 that was 104,257 ops, 7m49s under machine load,
// and the board refused connections for eleven minutes. The person decided
// what is kept: ledger history for 24 hours by default, everything older
// folded into the snapshot ([ledger] history).
//
// File: one plaintext JSON header line, then the gob-encoded core.State
// sealed with the ledger's own key. The header is plaintext so `dibs verify`
// can anchor the hash chain without the key; the state is sealed because it
// holds what the ledger seals (bodies, tokens) in the clear.

// SnapshotName is the snapshot's file name beside ledger.jsonl.
const SnapshotName = "snapshot.dibs"

const (
	snapshotVersion        = 1
	featureSnapshotVersion = 2
)

// SnapshotHeader anchors a snapshot in the hash chain.
type SnapshotHeader struct {
	Version  int       `json:"version"`
	Serial   uint64    `json:"serial"` // the last ledger serial folded in
	Head     string    `json:"head"`   // hash of that serial's ledger line: the next line's prev
	Node     string    `json:"node"`
	Created  time.Time `json:"created"`
	Features []string  `json:"features,omitempty"`
}

func snapshotPath(ledgerPath string) string {
	return filepath.Join(filepath.Dir(ledgerPath), SnapshotName)
}

// writeSnapshot writes st durably: temp file, fsync, rename, directory fsync.
// It refuses to write a snapshot that does not decode back to st, because a
// snapshot is the only record of everything older than it once the ledger is
// compacted: a field the encoding dropped would be lost silently and for good.
func writeSnapshot(path string, h SnapshotHeader, st *core.State, box *Box) error {
	var enc bytes.Buffer
	if err := gob.NewEncoder(&enc).Encode(st); err != nil {
		return fmt.Errorf("encode snapshot: %w", err)
	}
	var back core.State
	if err := gob.NewDecoder(bytes.NewReader(enc.Bytes())).Decode(&back); err != nil {
		return fmt.Errorf("decode snapshot for verification: %w", err)
	}
	if !sameState(reflect.ValueOf(st).Elem(), reflect.ValueOf(&back).Elem()) {
		return errors.New("snapshot does not decode to the state it was written from; not written")
	}
	sealed, err := box.SealBytes(enc.Bytes())
	if err != nil {
		return err
	}
	h = snapshotHeaderForState(h, st)
	header, err := json.Marshal(h)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".snapshot-")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err = tmp.Write(append(append(header, '\n'), sealed...)); err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("write snapshot: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	return syncDir(dir)
}

// readSnapshotHeader reads only the plaintext header: no key needed.
func readSnapshotHeader(path string) (SnapshotHeader, bool, error) {
	// #nosec G304 -- fixed name beside the operator's ledger.
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return SnapshotHeader{}, false, nil
	}
	if err != nil {
		return SnapshotHeader{}, false, err
	}
	defer func() { _ = f.Close() }()
	line, err := bufio.NewReader(f).ReadBytes('\n')
	if err != nil {
		return SnapshotHeader{}, false, fmt.Errorf("snapshot header: %w", err)
	}
	var h SnapshotHeader
	if err := json.Unmarshal(line, &h); err != nil {
		return SnapshotHeader{}, false, fmt.Errorf("snapshot header: %w", err)
	}
	if err := validateSnapshotFeatures(h); err != nil {
		return SnapshotHeader{}, false, err
	}
	return h, true, nil
}

// readSnapshot loads the state a snapshot holds. ok is false when there is none.
func readSnapshot(path string, box *Box) (SnapshotHeader, *core.State, bool, error) {
	// #nosec G304 -- fixed name beside the operator's ledger.
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return SnapshotHeader{}, nil, false, nil
	}
	if err != nil {
		return SnapshotHeader{}, nil, false, err
	}
	nl := bytes.IndexByte(raw, '\n')
	if nl < 0 {
		return SnapshotHeader{}, nil, false, errors.New("snapshot has no header line")
	}
	h, _, err := readSnapshotHeader(path)
	if err != nil {
		return SnapshotHeader{}, nil, false, err
	}
	plain, err := box.OpenBytes(raw[nl+1:])
	if err != nil {
		return SnapshotHeader{}, nil, false, fmt.Errorf("open snapshot: %w", err)
	}
	st := new(core.State)
	if err := gob.NewDecoder(bytes.NewReader(plain)).Decode(st); err != nil {
		return SnapshotHeader{}, nil, false, fmt.Errorf("decode snapshot: %w", err)
	}
	if st.Serial != h.Serial {
		return SnapshotHeader{}, nil, false, fmt.Errorf("snapshot header says serial %d, its state %d", h.Serial, st.Serial)
	}
	return h, st, true, nil
}

func syncDir(dir string) error {
	if runtime.GOOS == "windows" {
		return nil // a directory cannot be opened for sync there; rename is already durable
	}
	// #nosec G304 -- the ledger's own directory.
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	if err := d.Sync(); err != nil && !errors.Is(err, os.ErrInvalid) {
		return err
	}
	return nil
}

// sameState compares two states the way the fold sees them: a nil and an
// empty map or slice are the same board (gob does not keep the difference),
// and times are compared as instants, not by their location pointers.
func sameState(a, b reflect.Value) bool {
	if a.Type() != b.Type() {
		return false
	}
	if t, ok := a.Interface().(time.Time); ok {
		return t.Equal(b.Interface().(time.Time))
	}
	switch a.Kind() {
	case reflect.Pointer, reflect.Interface:
		if a.IsNil() || b.IsNil() {
			return a.IsNil() == b.IsNil()
		}
		return sameState(a.Elem(), b.Elem())
	case reflect.Map:
		return sameMap(a, b)
	case reflect.Slice, reflect.Array:
		return sameElements(a, b)
	case reflect.Struct:
		return sameFields(a, b)
	default:
		return a.Equal(b)
	}
}

func sameMap(a, b reflect.Value) bool {
	if a.Len() != b.Len() {
		return false
	}
	for _, k := range a.MapKeys() {
		bv := b.MapIndex(k)
		if !bv.IsValid() || !sameState(a.MapIndex(k), bv) {
			return false
		}
	}
	return true
}

func sameElements(a, b reflect.Value) bool {
	if a.Len() != b.Len() {
		return false
	}
	for i := 0; i < a.Len(); i++ {
		if !sameState(a.Index(i), b.Index(i)) {
			return false
		}
	}
	return true
}

func sameFields(a, b reflect.Value) bool {
	for i := 0; i < a.NumField(); i++ {
		// Unexported fields are shape-guarded out of core.State
		// (TestEveryStateFieldCanBeCarriedByTheSnapshot).
		if a.Type().Field(i).IsExported() && !sameState(a.Field(i), b.Field(i)) {
			return false
		}
	}
	return true
}

// copyState is an independent copy of st through the snapshot encoding, so
// a fold can continue from a snapshot without sharing anything with it.
func copyState(st *core.State) (*core.State, error) {
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(st); err != nil {
		return nil, err
	}
	out := new(core.State)
	if err := gob.NewDecoder(io.Reader(&buf)).Decode(out); err != nil {
		return nil, err
	}
	return out, nil
}

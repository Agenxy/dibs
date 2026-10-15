// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package ledger

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// assertCompactionPreservesState is the snapshot's whole contract: state ==
// fold(ledger) holds with history folded into a snapshot, at any cutoff, on
// the first start that compacts, on every start after it, after appending,
// and after a compaction interrupted between writing the snapshot and
// trimming the ledger. Called from TestRandomizedReplayEquivalence, whose
// ledger exercises every op kind.
func assertCompactionPreservesState(t *testing.T, path string, want *core.State, from, to time.Time) {
	t.Helper()
	full, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	key, err := os.ReadFile(filepath.Join(filepath.Dir(path), "key"))
	if err != nil {
		t.Fatal(err)
	}
	_, fullHead, err := Verify(path)
	if err != nil {
		t.Fatal(err)
	}
	span := to.Sub(from)
	for _, frac := range []float64{0.25, 0.5, 0.9, 1.5} {
		dir := t.TempDir()
		lp := filepath.Join(dir, "ledger.jsonl")
		if err := os.WriteFile(lp, full, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "key"), key, 0o600); err != nil {
			t.Fatal(err)
		}
		cutoff := from.Add(time.Duration(frac * float64(span)))
		clock := func() time.Time { return cutoff.Add(time.Hour) }
		replay := func(keep time.Duration) (*Ledger, *core.State) {
			t.Helper()
			box, err := LoadOrCreateKey(filepath.Join(dir, "key"))
			if err != nil {
				t.Fatal(err)
			}
			led, err := Open(lp, "test", box)
			if err != nil {
				t.Fatal(err)
			}
			led.KeepHistory(keep)
			led.clock = clock
			st := core.NewState("test", core.DefaultLimits())
			if _, err := led.Replay(st); err != nil {
				t.Fatalf("cutoff %.2f: replay: %v", frac, err)
			}
			return led, st
		}

		led, got := replay(time.Hour)
		_ = led.Close()
		if !sameState(reflect.ValueOf(want).Elem(), reflect.ValueOf(got).Elem()) {
			t.Fatalf("cutoff %.2f: the compacting replay built a different board", frac)
		}
		snap, ok, err := readSnapshotHeader(filepath.Join(dir, SnapshotName))
		if err != nil || !ok {
			t.Fatalf("cutoff %.2f: no snapshot was written: %v", frac, err)
		}
		if info, _ := os.Stat(lp); info.Size() >= int64(len(full)) {
			t.Fatalf("cutoff %.2f: the ledger was not trimmed (%d bytes of %d)", frac, info.Size(), len(full))
		}
		if _, head, err := Verify(lp); err != nil || head != fullHead {
			t.Fatalf("cutoff %.2f: the compacted chain does not verify to the same head: %v", frac, err)
		}

		// Every later start: snapshot plus the kept lines.
		led, again := replay(time.Hour)
		serveHistory(t, led) // the mail-history view builds from the snapshot too
		_ = led.Close()
		if !sameState(reflect.ValueOf(want).Elem(), reflect.ValueOf(again).Elem()) {
			t.Fatalf("cutoff %.2f: replaying from the snapshot built a different board", frac)
		}

		// Interrupted: the snapshot was renamed, the trimmed ledger was not.
		trimmed, _ := os.ReadFile(lp)
		if err := os.WriteFile(lp, full, 0o600); err != nil {
			t.Fatal(err)
		}
		led, resumed := replay(0)
		_ = led.Close()
		if !sameState(reflect.ValueOf(want).Elem(), reflect.ValueOf(resumed).Elem()) {
			t.Fatalf("cutoff %.2f (snapshot %d): a full ledger beside a newer snapshot built a different board", frac, snap.Serial)
		}
		if after, _ := os.ReadFile(lp); string(after) != string(trimmed) {
			t.Fatalf("cutoff %.2f: the interrupted compaction was not finished (%d bytes, want %d)", frac, len(after), len(trimmed))
		}

		// The chain continues from the kept head, including from an empty file.
		led, cont := replay(0)
		if _, err := applyE(cont, led, &core.Op{Kind: core.OpRegister, Name: "after-snapshot", NewToken: "tafter"}, to.Add(time.Minute)); err != nil {
			t.Fatalf("cutoff %.2f: append after compaction: %v", frac, err)
		}
		_ = led.Close()
		led, appended := replay(0)
		_ = led.Close()
		if !sameState(reflect.ValueOf(cont).Elem(), reflect.ValueOf(appended).Elem()) {
			t.Fatalf("cutoff %.2f: an op appended after compaction did not replay", frac)
		}
	}
}

// The snapshot carries core.State through gob, which encodes exported fields
// and nothing else. A field gob cannot carry would be lost at the first
// compaction and for good, silently: so the shape is guarded statically,
// beside the round-trip check writeSnapshot makes on every write.
func TestEveryStateFieldCanBeCarriedByTheSnapshot(t *testing.T) {
	seen := map[reflect.Type]bool{}
	var walk func(reflect.Type, string)
	walk = func(ty reflect.Type, path string) {
		if seen[ty] || ty == reflect.TypeOf(time.Time{}) {
			return
		}
		seen[ty] = true
		switch ty.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Array:
			walk(ty.Elem(), path+"[]")
		case reflect.Map:
			walk(ty.Key(), path+"{key}")
			walk(ty.Elem(), path+"{}")
		case reflect.Interface, reflect.Func, reflect.Chan, reflect.UnsafePointer:
			t.Errorf("%s is a %s: the snapshot cannot carry it", path, ty.Kind())
		case reflect.Struct:
			for i := 0; i < ty.NumField(); i++ {
				f := ty.Field(i)
				if !f.IsExported() {
					t.Errorf("%s.%s is unexported: the snapshot drops it", path, f.Name)
					continue
				}
				walk(f.Type, path+"."+f.Name)
			}
		default: // scalars and strings: gob carries them
		}
	}
	walk(reflect.TypeOf(core.State{}), "State")
}

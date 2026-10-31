// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package ledger

import (
	"bytes"
	"encoding"
	"encoding/gob"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// The snapshot's field names are FROZEN, and they are the Go names, not the
// json tags.
//
// writeSnapshot encodes core.State with encoding/gob, and gob matches fields
// by their Go identifier. It ignores a field it does not recognise and leaves
// a field it was not sent at zero, with no error either way. So a renamed
// field in anything reachable from State decodes from every snapshot written
// before the rename as zero, the daemon boots, and the verify-on-write check
// cannot see it, because it round-trips through one binary. With the ledger
// compacted after about a day, the snapshot IS the board for everything
// older than that.
//
// TestLedgerFieldNamesAreFrozen guards the json tags, which is what the
// ledger lines use, and AGENTS.md said a Go identifier may be renamed freely.
// That was true of ops and false of state. Found while a mutation of a new
// snapshot field's json tag survived: gob never reads the tag.
//
// Fingerprinted for the same reason as the tag list: a sweep that renames the
// field will happily rename a literal list beside it. Adding a field changes
// the fingerprint too, so a new field takes one deliberate edit here; a
// rename takes that edit plus an answer to "what happens to old snapshots".
func TestSnapshotGoFieldNamesAreFrozen(t *testing.T) {
	names := map[string]bool{}
	gobFieldNames(reflect.TypeFor[core.State](), map[reflect.Type]bool{}, names)
	if len(names) < 50 {
		t.Fatalf("setup: walked only %d fields from core.State; the walk is broken, not the guard", len(names))
	}
	// Traffic adds State.Traffic and 16 names in its four reachable types.
	// Removing those additions restores the preceding 8470c11c2a752407 hash;
	// no previously persisted Go field was renamed or removed.
	// Recovery adds address reservations, inherited history and typed consent.
	// All previously persisted Go names remain unchanged.
	// Desktop approval adds State.DesktopApproval and its Enabled/By/At
	// fields. Removing exactly these four names restores bc63c80a4b1e0eee;
	// every pre-existing persisted Go name remains unchanged.
	const want = "sha256:2de9989b836a0a0e"
	if got := fingerprint(names); got != want {
		sorted := make([]string, 0, len(names))
		for n := range names {
			sorted = append(sorted, n)
		}
		sort.Strings(sorted)
		t.Fatalf("snapshot Go field names changed: fingerprint %s, want %s.\n"+
			"A RENAMED or REMOVED field decodes as zero from every existing snapshot, silently. "+
			"Keep the old Go name, or make the daemon refuse old snapshots on purpose. "+
			"An ADDED field is safe: update the fingerprint.\n%s",
			got, want, strings.Join(sorted, "\n"))
	}
}

// The premise, measured rather than remembered: gob drops a renamed field
// without an error. If this ever fails, the guard above is guarding nothing.
func TestGobDropsARenamedFieldSilently(t *testing.T) {
	type before struct{ AdoptedAt, Kept uint64 }
	type after struct{ AdoptedSerial, Kept uint64 }
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(before{AdoptedAt: 7, Kept: 9}); err != nil {
		t.Fatal(err)
	}
	var got after
	if err := gob.NewDecoder(&buf).Decode(&got); err != nil {
		t.Fatalf("gob refused the rename (%v): the snapshot rule could be relaxed", err)
	}
	if got.AdoptedSerial != 0 || got.Kept != 9 {
		t.Fatalf("decoded %+v; want the renamed field zero and the kept one intact", got)
	}
}

// gobFieldNames records Type.Field for every field gob would encode in t and
// in every struct reachable from it, the way gob itself walks them.
func gobFieldNames(t reflect.Type, seen map[reflect.Type]bool, out map[string]bool) {
	for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice || t.Kind() == reflect.Array {
		t = t.Elem()
	}
	if t.Kind() == reflect.Map {
		gobFieldNames(t.Key(), seen, out)
		gobFieldNames(t.Elem(), seen, out)
		return
	}
	if t.Kind() != reflect.Struct || seen[t] {
		return
	}
	seen[t] = true
	// A type that encodes itself (time.Time) is opaque to gob's field matching.
	if t.Implements(reflect.TypeFor[gob.GobEncoder]()) ||
		reflect.PointerTo(t).Implements(reflect.TypeFor[gob.GobEncoder]()) ||
		t.Implements(reflect.TypeFor[encoding.BinaryMarshaler]()) ||
		reflect.PointerTo(t).Implements(reflect.TypeFor[encoding.BinaryMarshaler]()) {
		return
	}
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() || f.Type.Kind() == reflect.Chan || f.Type.Kind() == reflect.Func {
			continue // gob skips these
		}
		out[t.String()+"."+f.Name] = true
		gobFieldNames(f.Type, seen, out)
	}
}

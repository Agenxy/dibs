// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mailhistory

import (
	"encoding/binary"
	"reflect"
	"testing"
	"time"
)

// Drive the production codec with one independently changed leaf at a time,
// then revert it. A new field omitted from either codec half fails this guard
// even if a hand-maintained all-fields literal forgot to set the new field.
func TestDeltaSnapshotsPreserveEveryChangedLeafAndItsReversion(t *testing.T) {
	at := time.Date(2026, 10, 6, 12, 0, 0, 123, time.UTC)
	base := snapshotUnit{
		Position: position{17, 11, 1}, At: at, Kind: "send_message",
		Metadata: Metadata{From: "sender", To: "receiver", Type: "notify", State: "pending", SentAt: at},
		Author:   Author{ID: "sender", Created: 3, Host: "recorded-host"}, AfterKnown: true,
	}
	want := []snapshotUnit{base}
	for _, path := range historyLeafPaths(reflect.TypeFor[snapshotUnit](), nil) {
		changed := base
		v := reflect.ValueOf(&changed).Elem().FieldByIndex(path)
		switch {
		case v.Type() == reflect.TypeFor[time.Time]():
			v.Set(reflect.ValueOf(at.Add(42 * time.Second)))
		case v.Kind() == reflect.String:
			v.SetString(v.String() + "-changed")
		case v.Kind() == reflect.Bool:
			v.SetBool(!v.Bool())
		case v.Kind() == reflect.Int:
			v.SetInt(v.Int() - 17)
		case v.Kind() == reflect.Uint64:
			v.SetUint(1<<63 + v.Uint() + 7)
		case v.Kind() == reflect.Uint32:
			v.SetUint(1<<32 - 1)
		default:
			t.Fatalf("new history field type needs a round-trip fixture: %v", v.Type())
		}
		want = append(want, changed, base)
	}
	var c codec
	for _, u := range want {
		if err := c.add(u); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.flush(); err != nil {
		t.Fatal(err)
	}
	if len(c.blocks) != 1 {
		t.Fatal("leaf fixture must stay within one self-contained block")
	}
	b := c.blocks[0]
	got, err := decode(b.data, b.raw, b.count)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("changed or reverted metadata lost: %v", err)
	}
}

func TestCanonicalComparisonAllocatesNothing(t *testing.T) {
	u := snapshotUnit{Position: position{17, 11, 1}, Metadata: Metadata{RequestPriority: "urgent"}}
	previous := u
	previous.Position.Op = 16
	previous.Metadata.RequestPriority = "normal"
	var mask uint64
	if n := testing.AllocsPerRun(1000, func() { mask = changedFields(&u, &previous) }); n != 0 {
		t.Fatalf("canonical comparison allocated %v times per unit", n)
	}
	raw, err := encodeUnit(nil, u, &previous)
	if err != nil || binary.LittleEndian.Uint64(raw[1:9]) != mask || mask != 1|1<<32 {
		t.Fatal("production encoder did not consume the canonical change mask", err)
	}
}

func historyLeafPaths(typ reflect.Type, prefix []int) [][]int {
	if typ == reflect.TypeFor[time.Time]() || typ.Kind() != reflect.Struct {
		return [][]int{prefix}
	}
	var result [][]int
	for n := 0; n < typ.NumField(); n++ {
		path := append(append([]int(nil), prefix...), n)
		result = append(result, historyLeafPaths(typ.Field(n).Type, path)...)
	}
	return result
}

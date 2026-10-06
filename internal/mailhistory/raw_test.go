package mailhistory

import (
	"reflect"
	"testing"
	"time"
)

func TestNativeCapturePreservesEveryChangedLeafAndReversion(t *testing.T) {
	base := snapshotUnit{Metadata: Metadata{From: "sender", To: "receiver", RequestPriority: "urgent"}}
	var want []snapshotUnit
	for _, path := range historyLeafPaths(reflect.TypeFor[snapshotUnit](), nil) {
		u := base
		v := reflect.ValueOf(&u).Elem().FieldByIndex(path)
		switch {
		case v.Type() == reflect.TypeFor[time.Time]():
			v.Set(reflect.ValueOf(time.Date(2026, 10, 6, 1, 2, 3, 4, time.UTC)))
		case v.Kind() == reflect.String:
			v.SetString(v.String() + "changed")
		case v.Kind() == reflect.Bool:
			v.SetBool(true)
		case v.Kind() == reflect.Int:
			v.SetInt(-17)
		case v.Kind() == reflect.Uint64:
			v.SetUint(1<<63 + 7)
		case v.Kind() == reflect.Uint32:
			v.SetUint(1<<32 - 1)
		default:
			t.Fatalf("fixture needs new field type: %v", v.Type())
		}
		want = append(want, u, base)
	}
	c := newRawChunk()
	for _, u := range want {
		c.add(u)
	}
	c.seal()
	if c.stringIDs != nil || c.timeIDs != nil || c.previous != (snapshotUnit{}) {
		t.Fatal("sealed capture retained writer scratch")
	}
	var previous snapshotUnit
	for n, expected := range want {
		u := c.unit(uint32(n), previous) // #nosec G115 -- bounded fixture below blockUnits
		if u != expected {
			t.Fatalf("native field %d lost or failed to revert", n)
		}
		previous = u
	}
}

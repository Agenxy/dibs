package mailhistory

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestCompactSnapshotsRetainEveryFieldAndSplitOnActualBytes(t *testing.T) {
	at := time.Date(2026, 10, 6, 6, 0, 0, 12345, time.UTC)
	u := snapshotUnit{
		Position: position{30, 10, 1}, At: at, Kind: "message.test",
		Metadata: Metadata{
			From: "sender", To: "heir", Type: "request", State: "done", Consumed: true,
			AdoptedFrom: "previous", AdoptedAt: 20, Delivered: 21, Responded: 22, Acked: 23,
			OutcomeRead: 24, ReviewRead: 25, ReviewCutoff: 26, Superseded: 27,
			SentAt: at, DeliveredAt: at.Add(time.Second), TerminalAt: at.Add(2 * time.Second),
			RetainUntil: at.Add(time.Hour), Deadline: at.Add(time.Minute), QueuePriority: "high",
			QueueRank: 3, QueueDebt: true, QueueOrderLocked: true, QueueChanged: 28, Milestones: 4, Progress: 2,
		},
		Author: Author{ID: "heir", Created: 19, Host: "recorded-host"}, Content: true, AfterKnown: true, Evicted: true,
	}
	var c codec
	for n := 0; n < 130; n++ {
		if err := c.add(u); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.flush(); err != nil {
		t.Fatal(err)
	}
	for n := uint64(0); n < c.units; n++ {
		got, err := c.unit(n)
		if err != nil || !reflect.DeepEqual(got, u) {
			t.Fatalf("all-field round trip %d: %+v %v", n, got, err)
		}
	}
	if len(c.blocks) != 1 {
		t.Fatalf("all-field fixture should fit one bounded block: %d", len(c.blocks))
	}
	var sized codec
	u.Author.Host = strings.Repeat("h", 70000)
	for range 2 {
		if err := sized.add(u); err != nil {
			t.Fatal(err)
		}
	}
	if len(sized.blocks) != 1 || sized.count != 1 {
		t.Fatal("byte bound must split before unit count")
	}
	got, err := sized.unit(1)
	if err != nil || !reflect.DeepEqual(got, u) {
		t.Fatal("live tail round trip:", err)
	}
	u.Author.Host = strings.Repeat("h", blockBytes)
	if err := sized.add(u); err == nil {
		t.Fatal("oversized metadata was silently retained")
	}
	if _, err := c.unit(c.units); err == nil {
		t.Fatal("out-of-range reference accepted")
	}
	if _, err := decode([]byte("corrupt"), 7, 1); err == nil {
		t.Fatal("corrupt block accepted")
	}
}

func TestCompactBlocksSplitOnUnitLimit(t *testing.T) {
	var c codec
	for range blockUnits + 1 {
		if err := c.add(snapshotUnit{}); err != nil {
			t.Fatal(err)
		}
	}
	if len(c.blocks) != 1 || c.blocks[0].count != blockUnits || c.count != 1 {
		t.Fatal("unit limit did not split a block that was below its byte limit")
	}
	if _, err := c.unit(blockUnits); err != nil {
		t.Fatal("reference after the unit split:", err)
	}
}

func TestInheritedPrefixesStayFlatFrozenAndBounded(t *testing.T) {
	var a, b, c party
	a.refs.add(1)
	a.refs.add(2)
	if !b.inherit(&a) || !c.inherit(&b) || len(c.inherited) != 1 {
		t.Fatal("prefix was not flattened to a base vector")
	}
	a.refs.add(3)
	if c.inherited[0].length != 2 {
		t.Fatal("later source mail entered inherited prefix")
	}
	if !b.inherit(&a) || len(b.inherited) != 1 || b.inherited[0].length != 3 {
		t.Fatal("repeated transfer did not extend one deduplicated source")
	}
	if !a.inherit(&b) || len(a.inherited) != 0 {
		t.Fatal("returning mail created a cyclic party graph")
	}
	for n := 0; n < maxSources-1; n++ {
		v := &vector{}
		v.add(uint64(n))
		if !a.include(prefix{v, 1}) {
			t.Fatal("valid bounded source refused")
		}
	}
	extra := &vector{}
	extra.add(1)
	if a.include(prefix{extra, 1}) {
		t.Fatal("unbounded fanout admitted")
	}
}

func TestReferencePlanesRetainFullWidthAndClearHighWords(t *testing.T) {
	var v vector
	for n := 0; n < vectorChunk+1; n++ {
		v.add(uint64(n))
	}
	if v.chunks[0].high != nil || v.chunks[1].high != nil {
		t.Fatal("small references allocated a high-word plane")
	}
	for _, value := range []uint64{1<<32 - 1, 1 << 32, 1<<63 + 7, ^uint64(0), 3} {
		v.set(1, value)
		v.set(vectorChunk, value)
		if v.get(1) != value || v.get(vectorChunk) != value || v.get(0) != 0 || v.get(2) != 2 {
			t.Fatalf("wide reference changed or corrupted a neighbour: %d", value)
		}
	}
}

package main

import (
	"slices"
	"strconv"
	"testing"

	"github.com/agenxy/dibs/internal/overlap"
)

// Publishing a finished index is atomic with the check that the tree is
// still this build's.
//
// Round thirty-five closed the case where a build finished after its tree
// was evicted, by checking the claim before installing. The check released
// discoverMu and the install took it again, which is the gap eviction runs
// in: eviction deletes the bookkeeping and removes the scorer under one
// hold of that lock, exactly so nothing observes the half-done state, and
// a build that slipped between the two steps reinstalled an index the
// ceiling no longer knew about and answered the shipper "accepted".
//
// The seam fires where the last thing can get in before publication. Under
// the two-step version the claim had already been read by then, so the
// stale build published anyway. Round thirty-seven of the pre-release
// review.
func TestAnIndexIsNotPublishedIntoATreeEvictedWhileItWasBuilt(t *testing.T) {
	eng, ctx := testEngine(t)
	f := &scorerFlags{
		indexed:      map[string]bool{},
		rootOf:       map[string]string{},
		supplied:     map[string]string{},
		suppliedAt:   map[string]string{},
		suppliedHost: map[string]string{},
	}
	// Nobody is on the board, so a pass run now evicts whatever this
	// shipment claimed a moment ago: the real sequence, with the timing
	// made deterministic.
	f.beforePublish = func() { f.evictIdleIndexes(ctx, eng) }

	out := f.installSupplied(ctx, eng, "/repo", "shipper", "", &overlap.Payload{Fingerprint: "fp-1"})
	if out["accepted"] != false {
		t.Errorf("a shipment whose tree was evicted mid-install was told %v", out)
	}
	if f.indexed["/repo"] {
		t.Error("the evicted tree is marked indexed again with no claim behind it")
	}
	if got := eng.IndexedRepos(); slices.Contains(got, "/repo") {
		t.Errorf("the engine holds an index for a tree the daemon evicted: %v; "+
			"bookkeeping and scorer have to agree, or the rebuild is never scheduled", got)
	}
	if f.suppliedAt["/repo"] != "" {
		t.Errorf("the evicted tree kept a supplied fingerprint (%q), so a repeat "+
			"shipment is acknowledged without installing anything", f.suppliedAt["/repo"])
	}
}

// And with nothing interfering the same shipment installs, so the guard
// above is measuring the race and not refusing everything.
func TestAShipmentWithNoInterferencePublishes(t *testing.T) {
	eng, ctx := testEngine(t)
	f := &scorerFlags{
		indexed:      map[string]bool{},
		rootOf:       map[string]string{},
		supplied:     map[string]string{},
		suppliedAt:   map[string]string{},
		suppliedHost: map[string]string{},
	}
	out := f.installSupplied(ctx, eng, "/repo", "shipper", "", &overlap.Payload{Fingerprint: "fp-1"})
	if out["accepted"] != true {
		t.Fatalf("an uncontested shipment was refused: %v", out)
	}
	if got := eng.IndexedRepos(); !slices.Contains(got, "/repo") {
		t.Fatalf("the engine has no index for an accepted shipment: %v", got)
	}
	if f.suppliedAt["/repo"] != "fp-1" {
		t.Fatalf("the accepted shipment recorded fingerprint %q", f.suppliedAt["/repo"])
	}
}

// The status that says a tree has a supplied index never outlives the
// index it describes.
//
// NoteSuppliedIndexFrom and the match status ran after the claim lock
// was released, so an eviction in between removed the scorer and left
// the status saying this root has a supplied index: the shipment
// answered accepted, matching was gone, and the bridge then SKIPPED
// re-shipping because the status still said its index was installed.
// Nothing recovers from that until a restart. Round thirty-seven made
// the index and the bookkeeping atomic and left the status outside;
// round fifty-one of the pre-release review found the gap.
//
// WHAT THIS TEST DOES AND DOES NOT SHOW. It races a shipment against an
// eviction three hundred times and checks the invariant after each pass:
// the daemon never says a root has a supplied index while holding no
// scorer for it. Against the previous commit it does NOT trip, in nine
// hundred passes: the window is two function calls wide and cannot be
// hit from outside. The defect was found by reading, the fix is
// structural (the status moves under the same lock as the index), and
// this stands as the guard against the ordering being taken apart again
// rather than as a reproduction.
func TestTheSuppliedStatusDoesNotOutliveTheIndexItDescribes(t *testing.T) {
	eng, ctx := testEngine(t)
	for i := range 300 {
		f := &scorerFlags{
			indexed:      map[string]bool{},
			rootOf:       map[string]string{},
			supplied:     map[string]string{},
			suppliedAt:   map[string]string{},
			suppliedHost: map[string]string{},
		}
		root := "/repo/" + strconv.Itoa(i)
		done := make(chan map[string]any, 1)
		go func() {
			done <- f.installSupplied(ctx, eng, root, "shipper", "",
				&overlap.Payload{Fingerprint: "fp-1"})
		}()
		// Nobody is on the board, so this evicts whatever the shipment
		// has just claimed, whenever it gets there.
		f.evictIdleIndexes(ctx, eng)
		out := <-done

		installed := slices.Contains(eng.IndexedRepos(), root)
		says := eng.IndexSuppliedBy(root) != ""
		if says && !installed {
			t.Fatalf("pass %d: the daemon says %s has a supplied index and holds no scorer "+
				"for it (shipment said %v): the next shipment is skipped because the status "+
				"claims one is installed, and matching never comes back", i, root, out["accepted"])
		}
	}
}

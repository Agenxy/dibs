package main

import (
	"slices"
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

package main

import (
	"strconv"
	"testing"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/overlap"
)

// A supplied index is held while the agent that shipped it works under its
// root, even when that agent registered from a subdirectory.
//
// Eviction maps each agent's directory to a root through rootOf, which
// discovery fills in when it resolves a tree. A tree the daemon cannot read
// is exactly the one an agent ships an index for, and discovery had failed
// on it, so an agent registered from /repo/subdir had no rootOf entry: the
// next pass at the repository ceiling read its supplied index as unused and
// dropped it, while status still reported the tree as supplied and the
// bridge, having shipped once, did not ship again. Found by the pre-release
// review, round three. A directory under an indexed root is that root's,
// and no git is needed to say so.
func TestASuppliedIndexIsNotEvictedUnderItsShipperInASubdirectory(t *testing.T) {
	eng, ctx := testEngine(t)
	res, err := eng.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: "shipper", Agent: &core.AgentInfo{CWD: "/repo/subdir"},
	})
	if err != nil {
		t.Fatal("setup:", err)
	}
	if _, err := eng.Do(ctx, &core.Op{Kind: core.OpAckBoard, Token: res["token"].(string)}); err != nil {
		t.Fatal("setup:", err)
	}
	f := &scorerFlags{
		indexed:  map[string]bool{"/repo": true},
		supplied: map[string]string{"/repo": "shipper"},
		rootOf:   map[string]string{"/repo": "/repo"}, // what installSupplied records: the root, not the cwd
	}
	eng.SetIndex("/repo", overlap.NewLexicalFromFiles(nil, nil), engine.MatchConfig{},
		engine.IndexInfo{SuppliedBy: "shipper"})

	if f.evictIdleIndexes(ctx, eng) {
		t.Fatal("the supplied index was evicted while its shipper is active in a subdirectory of its root")
	}
	if !f.indexed["/repo"] || f.supplied["/repo"] != "shipper" {
		t.Errorf("index bookkeeping lost: indexed=%v supplied=%v", f.indexed, f.supplied)
	}
}

// A shipment from another machine for a root already held for a first one
// is refused, whatever its fingerprint: "already installed" was answered to
// a host the scorer would then refuse the index to. Round four of the
// pre-release review.
func TestASecondMachinesShipmentIsRefusedNotDeduplicated(t *testing.T) {
	eng, ctx := testEngine(t)
	f := &scorerFlags{
		indexed:      map[string]bool{"/repo": true},
		supplied:     map[string]string{"/repo": "first"},
		suppliedAt:   map[string]string{"/repo": "fp-1"},
		suppliedHost: map[string]string{"/repo": "host-a"},
		rootOf:       map[string]string{"/repo": "/repo"},
	}
	out := f.installSupplied(ctx, eng, "/repo", "second", "host-b", &overlap.Payload{Fingerprint: "fp-1"})
	if out["accepted"] != false {
		t.Errorf("a second machine's shipment at the first's fingerprint was answered %v; the "+
			"index stays assigned to the first host and the scorer refuses it to the second", out)
	}
	// The same machine repeating the same history is still acknowledged.
	out = f.installSupplied(ctx, eng, "/repo", "first", "host-a", &overlap.Payload{Fingerprint: "fp-1"})
	if out["accepted"] != true {
		t.Errorf("the shipper repeating its own history was refused: %v", out)
	}
}

// A shipment at the repository ceiling evicts an index nobody is in before
// refusing. Eviction ran only on the local discovery path, which a remote
// tree never takes and an unreadable local one fails before reaching, so a
// fleet on supplied indexes could not match its seventeenth repository
// until a restart. Round ten of the pre-release review.
func TestAShipmentAtTheCeilingEvictsAnIdleIndexFirst(t *testing.T) {
	eng, ctx := testEngine(t)
	f := &scorerFlags{indexed: map[string]bool{}, rootOf: map[string]string{}}
	for i := 0; i < maxIndexedRepos; i++ {
		root := "/idle/" + strconv.Itoa(i)
		f.indexed[root] = true
		f.rootOf[root] = root
		eng.SetIndex(root, overlap.NewLexicalFromFiles(nil, nil), engine.MatchConfig{}, engine.IndexInfo{})
	}
	// Nobody is in any of them; a new tree ships.
	out := f.installSupplied(ctx, eng, "/fresh", "shipper", "", &overlap.Payload{Fingerprint: "fp"})
	if out["accepted"] != true {
		t.Fatalf("a shipment at the ceiling with every index idle was refused: %v", out)
	}
}

// Eviction keeps a tree an agent came back to between its snapshot and its
// deletion. The snapshot excluded R; an agent then resumed into R, discovery
// found R indexed and returned; the deletion that followed used the stale
// snapshot and R's agent lost matching with nothing scheduled to rebuild it.
// A pass now evicts only what nothing has claimed or found since it took its
// snapshot. Round ten of the pre-release review.
func TestEvictionKeepsATreeAnAgentReturnedToMidPass(t *testing.T) {
	eng, ctx := testEngine(t)
	f := &scorerFlags{indexed: map[string]bool{"/repo": true}, rootOf: map[string]string{"/repo": "/repo"}}
	eng.SetIndex("/repo", overlap.NewLexicalFromFiles(nil, nil), engine.MatchConfig{}, engine.IndexInfo{})
	f.afterSnapshot = func() {
		// The returning agent's discovery, on the cheap path: the tree is
		// indexed, so nothing to do but say it was found.
		f.discoverMu.Lock()
		f.touchLocked("/repo")
		f.discoverMu.Unlock()
	}
	if f.evictIdleIndexes(ctx, eng) {
		t.Fatal("the pass evicted a tree an agent had returned to after the snapshot")
	}
	if !f.indexed["/repo"] {
		t.Fatal("the returned-to tree is gone")
	}
	// And with nobody returning, the idle tree does go.
	f.afterSnapshot = nil
	if !f.evictIdleIndexes(ctx, eng) || f.indexed["/repo"] {
		t.Fatal("an idle tree nobody returned to was kept")
	}
}

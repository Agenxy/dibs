package main

import (
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

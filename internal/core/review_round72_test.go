package core

import (
	"fmt"
	"testing"
)

// Eviction dropped the alias and kept its entry in GuessedSessions, so that
// list grew without any bound while the aliases stayed at eight: replayable
// state accumulating forever, most of it naming ids the agent no longer holds.
func TestEvictingAnAliasEvictsItsProvenance(t *testing.T) {
	a := &Agent{ID: "worker", SessionID: "host-1"}
	for i := 0; i < 100; i++ {
		a.bindHarnessSessionAs(fmt.Sprintf("019ffe52-0eaf-7f60-81cc-%012d", i), true, true)
	}
	if got := len(a.SessionAliases); got != maxSessionAliases {
		t.Fatalf("setup: %d aliases, want the cap of %d", got, maxSessionAliases)
	}
	if got := len(a.GuessedSessions); got > maxSessionAliases {
		t.Errorf("%d guessed sessions against %d aliases: the provenance of every evicted id is "+
			"still here, so this replayable list grows without bound for the life of the agent",
			got, len(a.SessionAliases))
	}
	// What remains must describe what the agent actually holds.
	for _, g := range a.GuessedSessions {
		if !a.holdsSession(g) {
			t.Errorf("GuessedSessions names %q, which the agent does not hold", g)
		}
	}
}

// And a v0.0.6 op evicts nothing, because GuessedSession() is read when
// deciding whether a resume changed anything: dropping these on replay could
// stop an op advancing the serial where the original fold advanced it.
func TestAPreV007BindKeepsEvictedProvenance(t *testing.T) {
	a := &Agent{ID: "worker", SessionID: "host-1"}
	for i := 0; i < 20; i++ {
		a.bindHarnessSessionAs(fmt.Sprintf("019ffe52-0eaf-7f60-81cc-%012d", i), true, false)
	}
	if len(a.GuessedSessions) <= maxSessionAliases {
		t.Errorf("a historical bind pruned its provenance (%d entries): replaying a v0.0.6 "+
			"ledger would reconstruct a different board than the one it built",
			len(a.GuessedSessions))
	}
}

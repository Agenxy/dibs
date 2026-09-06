package main

import (
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// The persistent default rose to 64 this cycle, and a configuration that set
// only `max_agents = 32`, valid under every release to v0.0.6, was refused at
// startup for a persistent setting its author never wrote. An unset ceiling
// follows the total; a stated one above it is still refused.
func TestAnUnsetPersistentCeilingFollowsAnExplicitTotal(t *testing.T) {
	got, err := applyLimits(LimitsConfig{MaxAgents: 32}, core.DefaultLimits())
	if err != nil {
		t.Fatalf("`[limits] max_agents = 32` alone was refused: %v\n"+
			"  the operator said nothing about persistence and the daemon would not start", err)
	}
	if got.MaxPersistentAgents != 32 || got.MaxAgents != 32 {
		t.Errorf("limits = persistent %d of %d; the unset persistent ceiling must follow "+
			"the total down to 32", got.MaxPersistentAgents, got.MaxAgents)
	}
	if _, err := applyLimits(LimitsConfig{MaxAgents: 32, MaxPersistentAgents: 40}, core.DefaultLimits()); err == nil {
		t.Error("a STATED persistent ceiling above the total was accepted: it reads as " +
			"applied and does nothing")
	}
}

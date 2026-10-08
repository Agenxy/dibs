// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// Sending to the human is not pull-only: a person is reached by the desktop
// notification, and the warning written for agents told senders that nothing
// could wake "dibs web" and delivery waited on inbox or check_in.
func TestSendingToTheHumanIsNotCalledPullOnly(t *testing.T) {
	st := core.NewState("t", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	registered, _, err := st.Apply(&core.Op{
		Kind: core.OpRegister, Name: "maintainer", NewToken: "tok-h",
		Agent: &core.AgentInfo{Harness: "dibs web", Surface: "web"},
	}, t0Engine())
	if err != nil {
		t.Fatal("setup:", err)
	}
	// Names are labels, not addresses: registration may slug or suffix them.
	id, ok := registered["agent_id"].(string)
	if !ok || id == "" {
		t.Fatalf("setup: registration returned no agent id: %v", registered)
	}
	l := st.Agents[id]
	if l == nil {
		t.Fatalf("setup: registered agent %q is absent", id)
	}
	if e.PullOnlyNote(l) == "" {
		t.Fatal("setup: an unwakeable row with no human identity drew no note, so the " +
			"exemption below proves nothing")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)
	human, _, err := e.HumanAgent(ctx)
	if err != nil {
		t.Fatal("setup human action:", err)
	}
	if n := e.PullOnlyNoteFor(ctx, human); n != "" {
		t.Errorf("a send to the human carried the agent wake warning: %q", n)
	}
}

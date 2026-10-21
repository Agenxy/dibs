// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"fmt"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// A broadcast is one call and pays the rate limit once.
//
// Each leg was an ordinary send charged to the coordinator's bucket (burst 30),
// so a fleet larger than the burst could not be reached by its own
// coordinator: on 2026-10-18 a 41-agent survey broadcast reached 31 and
// reported the other ten E_RATE_LIMITED, among them the agents it most needed.
func TestABroadcastReachesAFleetLargerThanTheBurst(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	lead, err := e.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: "fleet-lead", Nonce: "n-lead",
		AgentKind: core.KindPersistent,
	})
	if err != nil {
		t.Fatalf("setup: register: %v", err)
	}
	token, _ := lead["token"].(string)
	if token == "" {
		t.Fatalf("setup: register returned no token: %v", lead)
	}
	if _, err := e.GrantRole(ctx, "fleet-lead", core.RoleCoordinator); err != nil {
		t.Fatalf("setup: grant: %v", err)
	}
	const fleet = 2 * rateBurst
	for i := range fleet {
		if _, err := e.Do(ctx, &core.Op{
			Kind: core.OpRegister, Name: fmt.Sprintf("worker-%d", i),
			Nonce: fmt.Sprintf("n-%d", i), AgentKind: core.KindPersistent,
		}); err != nil {
			t.Fatalf("setup: register worker %d: %v", i, err)
		}
	}

	res, err := e.Broadcast(ctx, token, core.MsgNotify, "survey")
	if err != nil {
		t.Fatalf("broadcast: %v", err)
	}
	if res["delivered"] != fleet || res["undelivered"] != nil {
		t.Errorf("broadcast delivered %v of %d, undelivered %v", res["delivered"], fleet, res["undelivered"])
	}
	// The broadcast cost one token, so the coordinator can still talk.
	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpSendMessage, Token: token, To: "worker-0",
		MsgType: core.MsgNotify, Body: "after",
	}); err != nil {
		t.Errorf("a send right after the broadcast: %v", err)
	}
}

// A broadcast is fleet direction; the person and the daemon's own reporting
// row are rows on the board, not agents. On 2026-10-21 a survey question
// reached the person as a desktop dialog asking them to answer it.
func TestABroadcastReachesAgentsButNeverThePersonOrTheReporter(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	lead, err := e.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: "fleet-lead", Nonce: "n-lead",
		AgentKind: core.KindPersistent,
	})
	if err != nil {
		t.Fatalf("setup: register: %v", err)
	}
	token, _ := lead["token"].(string)
	if _, err := e.GrantRole(ctx, "fleet-lead", core.RoleCoordinator); err != nil {
		t.Fatalf("setup: grant: %v", err)
	}
	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: "worker", Nonce: "n-worker",
		AgentKind: core.KindPersistent,
	}); err != nil {
		t.Fatalf("setup: register worker: %v", err)
	}
	// The person's and the reporter's rows through the paths that create them
	// in production, not a register (which refuses both identities).
	human, _, err := e.HumanAgent(ctx)
	if err != nil || human == "" {
		t.Fatalf("setup: the person's row: %q, %v", human, err)
	}
	reporter, _, err := e.dibsAgent(ctx)
	if err != nil || reporter == "" {
		t.Fatalf("setup: the reporter's row: %q, %v", reporter, err)
	}

	res, err := e.Broadcast(ctx, token, core.MsgQuestion, "survey")
	if err != nil {
		t.Fatalf("broadcast: %v", err)
	}
	if res["delivered"] != 1 {
		t.Errorf("delivered to %v, want exactly the one agent", res["delivered"])
	}
	for _, id := range []string{human, reporter} {
		for _, m := range e.state.Inbox(id) {
			if m.From == "fleet-lead" {
				t.Errorf("the broadcast reached %s, which is not an agent", id)
			}
		}
	}
}

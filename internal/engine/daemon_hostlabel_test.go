// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"testing"

	"github.com/agenxy/dibs/internal/core"
	hostdisplay "github.com/agenxy/dibs/internal/hostname"
)

func TestTheRealFaultReporterUsesTheDaemonsDisplayLabel(t *testing.T) {
	st := core.NewState("node", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)
	_, detach := e.AttachHumanRelay()
	defer detach()
	if _, _, err := e.HumanAgent(ctx); err != nil {
		t.Fatal("setup:", err)
	}
	e.ReportFault(ctx, Fault{Kind: "label-fixture", What: "fixture fault", Remedy: "fixture remedy"})
	var reporter, token string
	onLoop(t, ctx, e, func(st *core.State) {
		reporter = e.dibsRowLocked()
		if row := st.Agents[reporter]; row != nil {
			token = row.Token
		}
	})
	if reporter == "" || token == "" {
		t.Fatal("setup: actual ReportFault did not mint its reporter")
	}
	// Simulate a historical hostname after the real reporter mint. Nothing
	// sets a provenance flag; the nonce index must come from ReportFault.
	onLoop(t, ctx, e, func(st *core.State) { st.Agents[reporter].Agent.Host = "legacy-reporter-host" })
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "unknown-peer", Agent: &core.AgentInfo{Host: "unproved-host", Harness: "dibs", Surface: "daemon"}}); err != nil {
		t.Fatal("setup: unrelated unknown-host peer:", err)
	}
	board, err := e.Board(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, row := range board["agents"].([]map[string]any) {
		switch row["id"] {
		case reporter:
			found++
			if row["host"] != hostdisplay.Name() {
				t.Errorf("real reporter display=%v want %s", row["host"], hostdisplay.Name())
			}
			info := row["agent"].(*core.AgentInfo)
			if info.Host != "legacy-reporter-host" || info.HostID != "" {
				t.Errorf("raw legacy identity changed: %+v", info)
			}
		case "unknown-peer":
			found++
			if row["host"] != "unproved-host" {
				t.Errorf("harness label used as provenance: %v", row["host"])
			}
		}
	}
	if found != 2 {
		t.Fatalf("setup: found %d required rows", found)
	}
}

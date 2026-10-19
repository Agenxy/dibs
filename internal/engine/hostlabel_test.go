// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"fmt"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	hostdisplay "github.com/agenxy/dibs/internal/hostname"
)

func TestManyRowHostProjectionUsesOnlyTheSuppliedSnapshot(t *testing.T) {
	st := core.NewState("local-node", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	for i := range 40 {
		id := fmt.Sprintf("local-%d", i)
		st.Agents[id] = &core.Agent{ID: id, Agent: &core.AgentInfo{HostID: "local-node", Host: "network-kernel"}}
	}
	board := st.Board()
	e.labelBoardHosts(board, "snapshot-label")
	rows := board["agents"].([]map[string]any)
	if len(rows) != 40 {
		t.Fatalf("setup: board has %d rows", len(rows))
	}
	for _, row := range rows {
		if row["host"] != "snapshot-label" {
			t.Fatalf("projection looked up a name instead of using its snapshot: %v", row["host"])
		}
		info := row["agent"].(*core.AgentInfo)
		if info.Host != "network-kernel" || info.HostID != "local-node" {
			t.Fatalf("projection changed coordination identity: %+v", info)
		}
	}
}

func TestMachineLabelsAreDerivedFromTheNewestVisibleMember(t *testing.T) {
	st := core.NewState("ledger", core.DefaultLimits())
	e := &Engine{state: st, hostID: "fleet", hostAliases: map[string]bool{"old-local": true}}
	stamp := time.Unix(100, 0)
	st.Agents = map[string]*core.Agent{
		"a":     {ID: "a", Agent: &core.AgentInfo{HostID: "remote", Host: "alpha"}, LastCoordination: stamp, CreatedSerial: 1},
		"b":     {ID: "b", Agent: &core.AgentInfo{HostID: "remote", Host: "beta"}, LastCoordination: stamp, CreatedSerial: 2},
		"local": {ID: "local", Agent: &core.AgentInfo{HostID: "old-local", Host: "stale-local"}},
	}
	check := func(want string) {
		t.Helper()
		board := st.Board()
		e.labelBoardHosts(board, hostdisplay.Name())
		for _, row := range board["agents"].([]map[string]any) {
			if row["id"] == "local" {
				if row["host"] != hostdisplay.Name() {
					t.Errorf("local alias display=%v", row["host"])
				}
			} else if row["host"] != want {
				t.Errorf("display=%v want %s", row["host"], want)
			}
		}
		if st.Agents["a"].Agent.Host != "alpha" || st.Agents["local"].Agent.HostID != "old-local" {
			t.Fatal("display changed replayable identity")
		}
	}
	check("beta") // Same coordination time: newer creation wins.
	st.Agents["b"].CreatedSerial = 1
	check("alpha") // Equal time and creation: agent ID makes selection stable.
	st.Agents["a"].LastCoordination = stamp.Add(time.Second)
	check("alpha") // Most recently coordinated wins, without changing its raw label.
	st.Agents["a"].Status = core.StatusArchived
	check("beta") // Hidden archived members do not name the visible machine.
}

package engine

import (
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

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
		e.labelBoardHosts(board)
		for _, row := range board["agents"].([]map[string]any) {
			if row["id"] == "local" {
				if row["host"] != thisHost() {
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
	st.Agents["a"].LastCoordination = stamp.Add(time.Second)
	check("alpha") // Most recently coordinated wins, without changing its raw label.
	st.Agents["a"].Status = core.StatusArchived
	check("beta") // Hidden archived members do not name the visible machine.
}

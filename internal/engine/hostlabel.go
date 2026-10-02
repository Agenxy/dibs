package engine

import "github.com/agenxy/dibs/internal/core"

// Display only. Raw per-agent labels and the host IDs used for coordination
// remain untouched. Remote labels come from the newest coordinated member,
// not a timestamp of the label itself; the fold does not record that timestamp.
func (e *Engine) labelBoardHosts(b core.Result) {
	latest := map[string]*core.Agent{}
	rows, _ := b["agents"].([]map[string]any)
	for _, row := range rows {
		a := e.state.Agents[row["id"].(string)]
		if a == nil {
			continue
		}
		if a.Agent == nil || a.Agent.HostID == "" || a.Agent.Host == "" {
			continue
		}
		id := e.canonicalHost(a.Agent.HostID)
		old := latest[id]
		if old == nil || a.LastCoordination.After(old.LastCoordination) ||
			(a.LastCoordination.Equal(old.LastCoordination) &&
				(a.CreatedSerial > old.CreatedSerial || (a.CreatedSerial == old.CreatedSerial && a.ID < old.ID))) {
			latest[id] = a
		}
	}
	for _, row := range rows {
		a := e.state.Agents[row["id"].(string)]
		if a == nil || a.Agent == nil {
			continue
		}
		label := a.Agent.Host
		id := e.canonicalHost(a.Agent.HostID)
		if id != "" {
			if id == e.HostID() || id == e.state.NodeID || e.hostAliases[id] {
				label = thisHost()
			} else if newest := latest[id]; newest != nil {
				label = newest.Agent.Host
			}
		}
		if label != "" {
			row["host"] = label
		}
	}
}

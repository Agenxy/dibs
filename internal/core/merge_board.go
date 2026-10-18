// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package core

func agentHostID(a *Agent) string {
	if a.Agent == nil {
		return ""
	}
	return a.Agent.HostID
}

// Possible duplicates are a view, not a decision to redirect anybody's mail.
// This uses the same pure coordinator rule; process absence is still checked
// freshly by the engine before execution, not inferred from a dormant label.
func (s *State) decorateMergeCandidates(rows []map[string]any) {
	for _, row := range rows {
		id, _ := row["id"].(string)
		a := s.Agents[id]
		var candidates []Result
		for _, other := range rows {
			to, _ := other["id"].(string)
			b := s.Agents[to]
			if id == to || a.Name == "" || a.Name != b.Name {
				continue
			}
			err := s.MergeEligibility(id, to, MergeEvidence{Known: a.PID == 0})
			candidate := Result{"into": to, "pair_eligible": err == nil}
			if err != nil {
				candidate["reason"] = err.Error()
			}
			candidates = append(candidates, candidate)
		}
		if len(candidates) > 0 {
			row["possible_forks"] = candidates
		}
	}
}

func (s *State) mergedBoard() []Result {
	var rows []Result
	for _, id := range sortedKeys(s.Agents) {
		a := s.Agents[id]
		if a.MergedInto == "" {
			continue
		}
		row := Result{"id": id, "name": a.Name, "into": a.MergedInto, "by": a.MergedBy}
		if !a.MergedAt.IsZero() {
			row["at"] = a.MergedAt
		}
		rows = append(rows, row)
	}
	return rows
}

func (s *State) mergeSourcesBoard() []Result {
	var rows []Result
	for _, id := range sortedKeys(s.Agents) {
		a := s.Agents[id]
		if a.Gone() && a.MergedInto == "" {
			rows = append(rows, Result{"id": id, "name": a.Name, "status": a.Status})
		}
	}
	return rows
}

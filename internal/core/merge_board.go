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
	// A board can contain hundreds of unrelated roles. Register also returns a
	// board during replay, so comparing every row with every other row makes
	// boot history cubic in fleet size. Preserve row order within each name;
	// host and harness mismatches still belong in the output with their refusal.
	byName := make(map[string][]*Agent)
	for _, row := range rows {
		id, _ := row["id"].(string)
		a := s.Agents[id]
		if a.Name != "" {
			byName[a.Name] = append(byName[a.Name], a)
		}
	}
	for _, row := range rows {
		id, _ := row["id"].(string)
		a := s.Agents[id]
		var candidates []Result
		for _, b := range byName[a.Name] {
			to := b.ID
			if id == to {
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

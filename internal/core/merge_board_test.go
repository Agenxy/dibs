// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package core

import (
	"fmt"
	"math/rand/v2"
	"reflect"
	"testing"
	"time"
)

func TestBoardMergeCandidatesPreserveEveryPairAndReason(t *testing.T) {
	for seed := uint64(0); seed < 20; seed++ {
		rng := rand.New(rand.NewPCG(seed, seed+1))
		s := NewState("merge-board", DefaultLimits())
		for n := 0; n < 100; n++ {
			id := fmt.Sprintf("agent-%03d", n)
			a := &Agent{
				ID: id, Name: fmt.Sprintf("role-%d", rng.IntN(30)), Kind: KindPersistent,
				Status: []AgentStatus{StatusActive, StatusDormant, StatusClosed, StatusArchived}[rng.IntN(4)],
				PID:    rng.IntN(2), LastCoordination: time.Unix(int64(rng.IntN(10)), 0),
				Agent: &AgentInfo{HostID: fmt.Sprintf("host-%d", rng.IntN(3)), Harness: fmt.Sprintf("harness-%d", rng.IntN(3)), Surface: "cli"},
			}
			if n%7 == 0 {
				a.Agent = nil
			}
			if n%11 == 0 {
				a.Name = ""
			}
			if n%13 == 0 {
				a.SessionID = "bound"
			}
			if n%17 == 0 {
				a.Slots = map[string]Slot{"s1": {ID: "s1", Text: "working"}}
			}
			s.Agents[id] = a
		}
		// Cross-host and cross-harness pairs must remain visible with reasons.
		for n := 0; n < 3; n++ {
			id := fmt.Sprintf("cross-%d", n)
			s.Agents[id] = &Agent{
				ID: id, Name: "same-role", Kind: KindPersistent, Status: StatusDormant,
				Agent: &AgentInfo{HostID: fmt.Sprintf("host-%d", n), Harness: fmt.Sprintf("harness-%d", n), Surface: "cli"},
			}
		}
		got := s.Board()["agents"].([]map[string]any)
		want := make([]map[string]any, len(got))
		for i, row := range got {
			want[i] = make(map[string]any, len(row))
			for key, value := range row {
				if key != "possible_forks" {
					want[i][key] = value
				}
			}
		}
		quadraticMergeCandidateOracle(s, want)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("seed %d: grouped board changed candidate selection, order or refusal", seed)
		}
	}
}

// Test-only oracle of the original projection. Production must not walk the
// entire fleet for each unrelated name; rejected same-name pairs still count.
func quadraticMergeCandidateOracle(s *State, rows []map[string]any) {
	for _, row := range rows {
		id := row["id"].(string)
		a := s.Agents[id]
		var candidates []Result
		for _, other := range rows {
			to := other["id"].(string)
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

// Enters through Board, the same path register takes during history replay.
func BenchmarkBoardDistinctMergeNames(b *testing.B) {
	for _, size := range []int{100, 300, 1000} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			s := NewState("merge-board", DefaultLimits())
			for n := 0; n < size; n++ {
				id := fmt.Sprintf("role-%d", n)
				s.Agents[id] = &Agent{ID: id, Name: id, Status: StatusDormant, Kind: KindPersistent}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				s.Board()
			}
		})
	}
}

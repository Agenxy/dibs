// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package core

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

func TestBoardReferenceFieldsDoNotAliasState(t *testing.T) {
	for _, field := range []string{"slot-dirs", "slot-refs", "slot-holds", "aliases", "guessed", "permissions", "relocated", "claim", "claim-repo", "queue", "evidence"} {
		t.Run(field, func(t *testing.T) {
			s := NewState("snapshot", DefaultLimits())
			ackReg(t, s, "worker", "worker", time.Unix(1700000000, 0))
			a := s.Agents["worker"]
			a.Slots = map[string]Slot{"s1": {ID: "s1", Dirs: []string{"dir"}, Refs: []string{"ref"}, Holds: []string{"hold"}}}
			a.SessionAliases = []string{"alias"}
			a.GuessedSessions = []string{"guess"}
			a.Permissions = []string{"permission"}
			a.Relocated = &Relocation{By: "actor"}
			s.Claims = []*Claim{{Agent: "worker", Path: "path", Repo: &RepoIdentity{RepoRoots: "roots"}}}
			s.Spaces["space"] = &Space{ID: "space", Queue: []string{"queued"}, Members: map[string]*Membership{"worker": {Agent: "worker", Evidence: []string{"evidence"}}}}
			board := s.Board()
			before, err := json.Marshal(s)
			if err != nil {
				t.Fatal(err)
			}
			row := board["agents"].([]map[string]any)[0]
			sessions := row["sessions"].(map[string]any)
			slot := row["slots"].([]Slot)[0]
			space := board["spaces"].([]map[string]any)[0]
			switch field {
			case "slot-dirs":
				slot.Dirs[0] = "changed"
			case "slot-refs":
				slot.Refs[0] = "changed"
			case "slot-holds":
				slot.Holds[0] = "changed"
			case "aliases":
				sessions["aliases"].([]string)[0] = "changed"
			case "guessed":
				sessions["guessed"].([]string)[0] = "changed"
			case "permissions":
				row["permissions"].([]string)[0] = "changed"
			case "relocated":
				row["relocated"].(*Relocation).By = "changed"
			case "claim":
				board["claims"].([]*Claim)[0].Note = "changed"
			case "claim-repo":
				board["claims"].([]*Claim)[0].Repo.RepoRoots = "changed"
			case "queue":
				space["queue"].([]string)[0] = "changed"
			case "evidence":
				space["members"].([]map[string]any)[0]["evidence"].([]string)[0] = "changed"
			}
			after, err := json.Marshal(s)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("board reader mutated replayable state through its result")
			}
		})
	}
}

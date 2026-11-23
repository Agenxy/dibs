// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package assets

import (
	"os/exec"
	"testing"
)

func TestBoardRendererShowsRegistrationAndTurnEvidence(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("bun unavailable")
	}
	js := BoardJS() + `
const row = {id:"worker", status:"active", turn:"between_turns",
  turn_observed_at:new Date(Date.now() - 26 * 60000).toISOString()};
const roster = Board.rosterHTML([row]);
if (!roster.includes("Active registrations") || !roster.includes("Between turns") ||
    !roster.includes("Observed 26m ago.")) throw new Error(roster);
for (const [turn, label] of [["in_turn", "In turn"], ["unknown", "Turn unknown"]]) {
  const html = Board.laneHTML({...row, turn, turn_observed_at:null}, {pinned:true});
  if (!html.includes("Registered") || !html.includes(label)) throw new Error(html);
  if (turn === "unknown" && !html.includes("No evidence of turn state.")) throw new Error(html);
}
`
	if out, err := exec.Command(bun, "-e", js).CombinedOutput(); err != nil {
		t.Fatalf("registration or turn evidence hidden in board: %v: %s", err, out)
	}
}

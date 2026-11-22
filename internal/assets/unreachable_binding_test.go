// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package assets

import (
	"os/exec"
	"testing"
)

func TestRosterShowsUnreachableBindingAndRecovery(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("bun unavailable")
	}
	js := BoardJS() + `
const wake = "Start a local app thread and register with the saved nonce.";
const row = {id:"worker",status:"unreachable",stale_reason:"lease_lapsed",wake};
const html = Board.rosterHTML([row]);
if (!html.includes(">unreachable<span") || !html.includes(wake) || html.includes(">no contact<span")) {
  throw new Error(html);
}
`
	if out, err := exec.Command(bun, "-e", js).CombinedOutput(); err != nil {
		t.Fatalf("unreachable binding recovery hidden: %v: %s", err, out)
	}
}

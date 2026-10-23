// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package assets

import (
	"os/exec"
	"testing"
)

func TestRosterShowsMailboxCapacityWarning(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("bun unavailable")
	}
	js := BoardJS() + `
const html=Board.rosterHTML([{id:"worker",status:"active",mailbox_capacity:{depth:205,limit:256,near_capacity:true,hint:"call inbox()"}}]);
if (!html.includes("mailbox 205/256") || !html.includes("call inbox()")) throw new Error(html);
`
	if out, err := exec.Command(bun, "-e", js).CombinedOutput(); err != nil {
		t.Fatalf("mailbox warning hidden: %v: %s", err, out)
	}
}

// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package assets

import (
	"os/exec"
	"testing"
)

func TestContactRendererShowsPostingTimeWithoutClaimingMailWasRead(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("bun unavailable")
	}
	js := BoardJS() + `
const stamp = "2026-10-09T17:00:00Z";
const html = Board.contactAlertsHTML([{recipient:"worker", oldest_serial:42, count:3, notified_at:stamp}]);
if (!html.includes(stamp) || !html.includes("posted") || !html.includes("still unread") || html.includes("not yet confirmed")) throw new Error(html);
const unposted = Board.contactAlertsHTML([{recipient:"worker", oldest_serial:42, count:3}]);
if (!unposted.includes("not yet confirmed") || unposted.includes("posted at")) throw new Error(unposted);
const escaped = Board.contactAlertsHTML([{recipient:"worker", notified_at:"<script>"}]);
if (escaped.includes("<script>") || !escaped.includes("&lt;script&gt;")) throw new Error(escaped);
`
	if out, err := exec.Command(bun, "-e", js).CombinedOutput(); err != nil {
		t.Fatalf("shared contact renderer lost posting evidence: %v: %s", err, out)
	}
}

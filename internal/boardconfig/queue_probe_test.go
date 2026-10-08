// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package boardconfig

import "testing"

func TestQueueProbeCannotHostAnAgent(t *testing.T) {
	safe := []string{"/bin/codex", "app-server", "--listen", "stdio://"}
	if !ReadOnlyQueueProbe(safe) {
		t.Fatal("read-only transport rejected")
	}
	for _, bad := range [][]string{{"codex", "app-server", "resume"}, {"codex", "app-server", "--listen", "stdio://", "exec"}, {"codex", "exec", "resume", "thread"}, {"codex", "app-server", "--listen", "stdio://", "start"}} {
		if ReadOnlyQueueProbe(bad) {
			t.Fatalf("hosting argv accepted: %v", bad)
		}
	}
	if HostsAnAgent(safe) == "" {
		t.Fatal("observer exemption leaked into operator wake routes")
	}
}

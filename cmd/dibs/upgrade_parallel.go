// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"fmt"

	"github.com/agenxy/dibs/internal/paths"
)

// A conflicting registry can come from an older daemon or an inconsistent
// claim. Refuse before stop: silently granting -allow-parallel would change
// the operator's policy. A legitimate parallel daemon retains its own flag.
func (was daemonState) parallelPolicyError() error {
	if was.parallel || len(was.restartBlockers) == 0 {
		return nil
	}
	other := was.restartBlockers[0]
	if other.Unknown {
		return fmt.Errorf("a live daemon registry entry is unreadable; nothing was stopped; "+
			"run dibs doctor and inspect %q before retrying the upgrade", paths.RunRegistryDir())
	}
	return fmt.Errorf("a non-parallel daemon is registered: pid %d, dir %q, addr %q; nothing was stopped; "+
		"stop that board with dibs stop, setting DIBS_DIR to %q, or restart it with -allow-parallel "+
		"if it is meant to be a second board; this daemon's flags were left unchanged",
		other.PID, other.Dir, other.Addr, other.Dir)
}

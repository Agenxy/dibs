// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"path/filepath"

	"github.com/agenxy/dibs/internal/boardconfig"
)

func reportRemovedWakeCooldowns(dir string, cfg boardconfig.Config, warn fixFn) {
	for _, key := range cfg.RetiredWakeCooldowns {
		warn(key.Warning(filepath.Join(dir, "dibs.toml")),
			"Run dibs upgrade to back up the file and remove the obsolete line, or delete the line yourself.")
	}
}

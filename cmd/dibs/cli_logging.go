// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"log/slog"
	"os"
)

// Read once at CLI main entry, like dibd. stdout belongs to the command (or
// JSON-RPC for mcp-stdio), so diagnostics always use stderr.
func configureCLILogging() {
	if os.Getenv("DIBS_LOG_DEBUG") != "" {
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})))
	}
}

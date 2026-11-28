// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/agenxy/dibs/internal/build"
	"github.com/agenxy/dibs/internal/clientdrift"
	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/mcp"
	"github.com/agenxy/dibs/internal/notify"
	"github.com/agenxy/dibs/internal/plugins"
)

type clientDriftWarning struct {
	kind string
	what string
	fix  string
}

var (
	clientDriftNotificationAvailable = notify.Available
	postClientDriftNotification      = notify.Banner
)

func localClientDrift() []clientDriftWarning {
	var warnings []clientDriftWarning
	home, _ := os.UserHomeDir()
	if older, shipped := plugins.OlderClaudeVersions(home); len(older) > 0 {
		warnings = append(warnings, clientDriftWarning{
			"claude-plugin",
			fmt.Sprintf("Claude Code's Dibs plugin includes older versions (%s); this build ships %s",
				strings.Join(older, ", "), shipped),
			plugins.ClaudeUpdate,
		})
	}
	if home, err := plugins.CodexHome(); err == nil {
		if installed, current, _ := plugins.CodexRootStatus(home, mcp.ToolsFingerprint()); installed && !current {
			warnings = append(warnings, clientDriftWarning{
				"codex-plugin",
				"Codex's Dibs plugin does not have a verified current version root",
				plugins.CodexUpdate,
			})
		}
	}
	for _, binary := range clientdrift.OlderBinaries(os.Getenv("PATH"), build.Version) {
		warnings = append(warnings, clientDriftWarning{"path-binary", binary.Description(), clientdrift.PathUpdate})
	}
	return warnings
}

func monitorClientDrift(ctx context.Context, eng *engine.Engine, dir string) {
	seen := make(map[clientDriftWarning]bool)
	hooks := codexTrustObserver()
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for ctx.Err() == nil {
		current := make(map[clientDriftWarning]bool)
		warnings := append(localClientDrift(), hooks.observe(ctx)...)
		for _, warning := range warnings {
			current[warning] = true
			if !seen[warning] {
				slog.Warn(warning.what, "client_kind", warning.kind, "fix", warning.fix)
			}
			notifyClientDrift(dir, warning)
		}
		seen = current
		eng.PruneBridgeBuilds(ctx)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// Reserve before posting: an ambiguous notification result or daemon restart
// must not send the same kind/build notice again. The local WARN remains the
// evidence if posting fails. No bridge notice enters this path.
func notifyClientDrift(dir string, warning clientDriftWarning) {
	if !clientDriftNotificationAvailable() {
		return
	}
	state := filepath.Join(dir, "client-version-notices")
	if err := os.MkdirAll(state, 0o700); err != nil { // #nosec G703 -- daemon-owned data directory
		return
	}
	key := sha256.Sum256([]byte(warning.kind + "\x00" + build.Version))
	path := filepath.Join(state, fmt.Sprintf("%x", key))
	// #nosec G304 G703 -- digest under daemon-owned data directory
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return
	}
	_ = f.Close()
	if err := postClientDriftNotification("Dibs client version warning", "", warning.what+"\n"+warning.fix); err != nil {
		slog.Debug("client version notice could not be posted", "client_kind", warning.kind, "err", err)
	}
}

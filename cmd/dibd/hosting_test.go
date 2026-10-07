// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/boardconfig"
)

// The daemon runs only commands that deliver, whatever the config says.
//
// Entered through the daemon's own loader, not the filter alone: a guard that
// nothing calls is how #245 shipped a feature called from nowhere.
func TestTheDaemonNeverLoadsACommandThatHostsAnAgent(t *testing.T) {
	cmds := wakeCommandsFrom(map[string]boardconfig.WakeExec{
		"codex": {
			Argv:     []string{"codex", "exec", "resume", "--skip-git-repo-check", "{thread}", "{message}"},
			Fallback: []string{"codex", "queue", "--thread", "{thread}", "--message", "{message}"},
		},
		"claude code": {Argv: []string{"claude", "--resume", "{thread}", "-p", "{message}"}},
	})
	c, ok := cmds["codex"]
	if !ok || c.Argv[1] != "queue" || len(c.Fallback) != 0 {
		t.Errorf("the daemon would run %+v for Codex; want `codex queue` alone. `exec resume` "+
			"runs the operator's thread in a headless Codex of Dibs' own", c)
	}
	if _, kept := cmds["claude code"]; kept {
		t.Error("the daemon loaded `claude --resume -p`, which runs a Claude Code session of its own")
	}
	for _, x := range cmds {
		for _, a := range append(append([]string{}, x.Argv...), x.Fallback...) {
			if a == "exec" || strings.HasSuffix(a, "claude") {
				t.Errorf("a hosting command survived into the daemon's table: %v / %v", x.Argv, x.Fallback)
			}
		}
	}
}

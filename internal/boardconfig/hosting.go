// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package boardconfig

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Dibs is a channel into the harness an agent already lives in. It never hosts
// one.
//
// IT DID. The documented Codex wake was `codex exec resume {thread}`, which
// delivers nothing to anybody: it starts a headless Codex of its own and runs
// the thread itself, outside the ChatGPT app the operator was using. Claude
// Code's was `claude --resume {thread} -p`, which does the same to a Claude Code
// session. doctor printed both, and the Codex plugin notes recommended the
// first. The operator found their ChatGPT threads running in a process Dibs had
// started and called it unacceptable: a board that runs agents has become a
// harness, with an operator's threads doing work nobody was watching, on a
// model allowance nobody approved for it.
//
// What a wake may do is put a message into a harness that is already running
// the agent: the session socket, the harness's own hooks, and for Codex the
// ChatGPT app's queue (`codex queue`), which hands the message to the thread
// the app holds. Nothing that starts the agent itself.
//
// Checked HERE because every wake command is read from here: the daemon
// (cmd/dibd) and `dibs host-bridge` on a joined machine both take their routes
// from DeliveringWakeRoutes, so neither can run a command the other refuses.
// Two loaders with a rule in one is this repository's most recurring bug.

// HostsAnAgent reports why argv would run an agent rather than deliver a message
// to one, or "" when it does not.
//
// The two harnesses Dibs has ever documented a command for. Codex has exactly
// one subcommand that delivers into a running app, `queue`, so it is an
// allowlist: anything else (`exec`, `resume`, the bare TUI) starts an agent.
// Claude Code has no command that delivers into a running session at all, so
// every `claude` invocation is refused; its sessions are reached through their
// own socket and hooks. An operator's own tool is not second-guessed: Dibs
// cannot know what it does, and the documented rule covers it.
func HostsAnAgent(argv []string) string {
	if len(argv) == 0 {
		return ""
	}
	switch filepath.Base(argv[0]) {
	case "codex":
		if sub := firstWord(argv[1:]); sub != "queue" {
			if sub == "" {
				sub = "with no subcommand"
			} else {
				sub = "`codex " + sub + "`"
			}
			return fmt.Sprintf("Codex %s runs the thread itself instead of delivering to "+
				"the ChatGPT app that holds it; the delivering command is `codex queue`", sub)
		}
	case "claude":
		return "`claude` starts a Claude Code session of its own; a running session is " +
			"reached through its socket and hooks, which need no wake command"
	}
	return ""
}

// ReadOnlyQueueProbe is the sole app-server invocation Dibs may construct.
// Resume/start/exec verbs and extra flags are never accepted. The caller sends
// only initialize, initialized and thread/queue/list over this transport.
func ReadOnlyQueueProbe(argv []string) bool {
	return len(argv) == 4 && filepath.Base(argv[0]) == "codex" && argv[1] == "app-server" &&
		argv[2] == "--listen" && argv[3] == "stdio://"
}

// firstWord is the first argument that is not a flag: the subcommand.
func firstWord(args []string) string {
	for _, a := range args {
		if a != "" && !strings.HasPrefix(a, "-") {
			return a
		}
	}
	return ""
}

// DeliveringWakeRoutes is the wake table with every command that would host an
// agent taken out, and why, by harness.
//
// REPAIRED WHERE IT CAN BE, not just rejected. The recipe Dibs used to
// recommend has `codex exec resume` as the command and `codex queue` as the
// fallback, and the fallback was always the right one. Every operator who
// followed that advice keeps a working wake with no edit to their dibs.toml:
// the hosting half goes, the delivering half becomes the command. An entry
// with nothing left that delivers is dropped, and said so, because a setting
// that is read and silently does nothing is worse than one that is refused.
func DeliveringWakeRoutes(exec map[string]WakeExec) (routes map[string]WakeExec, changed map[string]string) {
	routes = make(map[string]WakeExec, len(exec))
	changed = map[string]string{}
	for harness, x := range exec {
		argvWhy, fallbackWhy := HostsAnAgent(x.Argv), HostsAnAgent(x.Fallback)
		switch {
		case argvWhy == "" && (fallbackWhy == "" || len(x.Fallback) == 0):
			routes[harness] = x
		case argvWhy == "":
			x.Fallback = nil
			routes[harness] = x
			changed[harness] = "its fallback was dropped: " + fallbackWhy
		case len(x.Fallback) > 0 && fallbackWhy == "":
			x.Argv, x.Fallback = x.Fallback, nil
			routes[harness] = x
			changed[harness] = "its fallback is now its only command, because its command " +
				"would run an agent: " + argvWhy
		default:
			changed[harness] = "it was not loaded, because it would run an agent: " + argvWhy
		}
	}
	return routes, changed
}

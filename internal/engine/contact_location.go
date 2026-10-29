// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/harnessenv"
	hostdisplay "github.com/agenxy/dibs/internal/hostname"
)

// A registration's harness label is not an app, and a host ID is not a
// human destination. Use the same cached display projection as the board,
// without changing any identity or consulting an app on the writer.
func (e *Engine) contactOpenHint(name string, l *core.Agent) string {
	host, app, session, thread := "an unidentified machine", "its original harness", "", ""
	if l != nil {
		thread, session = threadIDOf(l), l.CurrentSession
		if session == "" {
			session = l.SessionID
		}
		if l.Agent != nil {
			label := e.boardHostLabel(l.Agent, nil, hostdisplay.Current().Name)
			if label != "" && label != l.Agent.HostID {
				host = label
			}
		}
		switch surfaceOf(l) {
		case harnessenv.ChatGPTApp:
			app = "ChatGPT"
		case harnessenv.ClaudeDesktop:
			app = "Claude"
		case harnessenv.CodexOutsideApp:
			app = "its terminal or IDE"
		}
	}
	location := name + " in " + app + " on " + host
	if thread != "" {
		return "open " + location + "; original session " + thread
	}
	if session != "" {
		return "no app thread is known for " + location + "; session " + session +
			"; find or restart the agent there, or reassign the work"
	}
	return "no thread or session is known for " + location + "; find or restart the agent there, or reassign the work"
}

// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/harnessenv"
	hostdisplay "github.com/agenxy/dibs/internal/hostname"
)

// A registration's harness label is not an app, and a host ID is not a
// human destination. Local contact instructions use this daemon's own
// friendly label, with a neutral fallback while the lookup is unavailable.
func (e *Engine) contactOpenHint(name string, l *core.Agent) string {
	host, app, session, thread := e.contactHost(l), "its original harness", "", ""
	if l != nil {
		thread, session = threadIDOf(l), l.CurrentSession
		if session == "" {
			session = l.SessionID
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
	if e.bindingNeedsRecovery(l) {
		reason := "The queue reported no rollout found for this binding."
		if surfaceOf(l) == harnessenv.ChatGPTCloudExec {
			reason = cloudBindingReason
		}
		return "Cannot wake " + name + " on " + host + ": " + reason + " " + bindingRemedy
	}
	if url := chatGPTContactURL(l); url != "" {
		return "Open " + location + " at " + url + " (original session " + thread + ")"
	}
	if thread != "" {
		return "open " + location + "; original session " + thread
	}
	if session != "" {
		return "no app thread is known for " + location + "; session " + session +
			"; find or restart the agent there, or reassign the work"
	}
	return "no thread or session is known for " + location + "; find or restart the agent there, or reassign the work"
}

func (e *Engine) contactHost(l *core.Agent) string {
	if l == nil || l.Agent == nil {
		return "an unidentified machine"
	}
	if e.ownsHost(l) && (l.Agent.HostID != "" || l.Agent.Host != "") {
		label := hostdisplay.Current()
		if label.Name != "" && !label.Fallback {
			return label.Name
		}
		return "this machine"
	}
	if l.Agent.HostID != "" && l.Agent.Host != "" && l.Agent.Host != l.Agent.HostID {
		return l.Agent.Host
	}
	return "an unidentified machine"
}

// The URL is descriptive on any host. An actionable desktop Open button
// still requires matching local provenance in enrichContactURL and the relay.
func chatGPTContactURL(l *core.Agent) string {
	if surfaceOf(l) != harnessenv.ChatGPTApp {
		return ""
	}
	argv := harnessenv.ChatGPTOpenArgv(threadIDOf(l))
	if argv == nil {
		return ""
	}
	return argv[len(argv)-1]
}

// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"fmt"
	"strings"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

func (e *Engine) outstandingUpdateCount(agent string) int {
	n := 0
	for _, g := range e.outcomeGroups(agent) {
		n += len(g.units)
	}
	for _, notice := range e.takeNotices(agent) {
		if !notice.ReadParent {
			n++
		}
	}
	return n
}

func (e *Engine) mailboxCountsText(agent string) string {
	c := e.mailCounts(agent)
	var parts []string
	for _, x := range []struct{ key, label string }{
		{"new", "new"},
		{"seen_unacknowledged_fyis", "FYIs seen but unacknowledged"},
		{"seen_awaiting_action", "seen messages awaiting action"},
		{"owed_requests", "owed requests"},
	} {
		if n := c[x.key].(int); n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, x.label))
		}
	}
	return strings.Join(parts, ", ")
}

// Natural activations recover outstanding state independently of one-shot
// wake freshness. No continuation, no human-facing field, and no read prefix
// is advanced for a compact pointer or shortened body.
func (e *Engine) passiveMailboxDigest(l *core.Agent, now time.Time) string {
	p, err := e.mailboxPage(l, "", 0, now)
	if err != nil {
		return ""
	}
	updates := e.outstandingUpdateCount(l.ID)
	announcements := len(e.state.Unacked(l.ID))
	if len(p.mail) == 0 && updates == 0 && announcements == 0 {
		return ""
	}
	var b strings.Builder
	counts := e.mailboxCountsText(l.ID)
	if counts == "" {
		counts = "outstanding coordination state"
	}
	fmt.Fprintf(&b, "Dibs: %s for your agent %q", counts, e.agentName(l.ID))
	if updates > 0 {
		fmt.Fprintf(&b, "; %d outstanding update units", updates)
	}
	if announcements > 0 {
		fmt.Fprintf(&b, "; %d unacknowledged announcements", announcements)
	}
	b.WriteString(".\n")
	for _, m := range p.mail {
		fmt.Fprintf(&b, "  #%d %s from %q, %s: read_mail(%d) has the full envelope.\n",
			m.Serial, m.Type, e.agentName(m.From), m.State, m.Serial)
	}
	if p.more > 0 {
		fmt.Fprintf(&b, "  %d more mailbox items; inbox returns compact pages and next_cursor.\n", p.more)
	}
	if updates > 0 || announcements > 0 {
		b.WriteString("  " + waitingReadHint(0, announcements, e.takeNotices(l.ID)) + "\n")
	}
	if e.mailCounts(l.ID)["seen_unacknowledged_fyis"].(int) > 0 {
		b.WriteString("  ack(seen_fyis:true) explicitly clears delivered FYIs; it leaves new mail and owed work intact.\n")
	}
	return strings.TrimSpace(b.String())
}

func (e *Engine) passiveHookOutput(l *core.Agent, event string, strict bool) core.Result {
	out := core.Result{"agent": l.ID, "queued": "passive recovery at a natural activation; wake freshness is unchanged"}
	if digest := e.passiveMailboxDigest(l, time.Now()); digest != "" {
		addDelivery(out, event, digest)
	}
	return e.hookOutput(out, strict, event)
}

func (e *Engine) quietHookOutput(l *core.Agent, event string, stopActive, strict bool) core.Result {
	if event == "SessionStart" {
		return e.passiveHookOutput(l, event, strict)
	}
	if cont := e.continuationReply(l, event, stopActive); cont != nil {
		return e.hookOutput(cont, strict, event)
	}
	// The agent id is needed by PreToolUse to resolve a subagent's parent.
	// No mail digest is injected on this quiet path.
	return e.hookOutput(core.Result{"agent": l.ID}, strict, event)
}

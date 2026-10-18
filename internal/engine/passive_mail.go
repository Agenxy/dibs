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
	return e.countsText(agent, true)
}

// mailboxNewsText is the same counts without the work the agent owes: what
// is waiting for it, not what it has already said it will do.
func (e *Engine) mailboxNewsText(agent string) string {
	return e.countsText(agent, false)
}

func (e *Engine) countsText(agent string, withOwed bool) string {
	c := e.mailCounts(agent)
	var parts []string
	for _, x := range []struct{ key, label string }{
		{"new", "new"},
		{"announced_fyis", "announced FYIs awaiting presentation"},
		{"seen_awaiting_action", "seen messages awaiting action"},
		{"owed_requests", "owed requests"},
	} {
		if x.key == "owed_requests" && !withOwed {
			continue
		}
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
	// Only mail that still wants the agent: a page fills its tail with
	// finished mail, and an expired question the agent had already read was
	// listed at every prompt with nothing it could do to clear it (k7-dev,
	// 2026-10-14).
	waiting := p.mail[:0:0]
	for _, m := range p.mail {
		if !m.Terminal() {
			waiting = append(waiting, m)
		}
	}
	p.mail = waiting
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
	shown := e.passiveMailLines(&b, p.mail)
	if p.more > 0 {
		fmt.Fprintf(&b, "  %d more mailbox items; inbox returns compact pages and next_cursor.\n", p.more)
	}
	if updates > 0 || announcements > 0 {
		b.WriteString("  " + waitingReadHint(0, announcements, e.takeNotices(l.ID)) + "\n")
	}
	// Reserve both hook carriers, including JSON escaping, even though a
	// passive activation normally needs only additionalContext.
	preview := core.Result{}
	addDelivery(preview, "Stop", b.String())
	if resultFitsFYIPresentation(preview) {
		if err := e.recordFYIPresentation(l, shown, true, now); err != nil {
			panic(err)
		}
	}
	return strings.TrimSpace(b.String())
}

func (e *Engine) passiveMailLines(b *strings.Builder, mail []*core.Message) []fyiPresentation {
	var shown []fyiPresentation
	for _, m := range mail {
		item := e.compactMail(m, time.Now())
		full := m.Type == core.MsgNotify && item["body_truncated"] != true
		shown = append(shown, fyiPresentation{serial: m.Serial, full: full})
		if full {
			fmt.Fprintf(b, "  #%d notify from %q: %q.\n", m.Serial, e.agentName(m.From), item["body"])
			continue
		}
		fmt.Fprintf(b, "  #%d %s from %q, %s: read_mail(%d) has the full envelope.\n",
			m.Serial, m.Type, e.agentName(m.From), m.State, m.Serial)
	}
	return shown
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

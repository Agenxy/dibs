package engine

import (
	"context"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/harnessenv"
	"github.com/agenxy/dibs/internal/humanask"
)

const (
	contactRetryInterval = 5 * time.Minute
	contactSettleDelay   = 2 * time.Second
	contactActivityGrace = 2 * time.Minute
)

// An active agent that just spoke may read mail on its own turn even when it
// has no external wake route. A finished hook overrides that recency. The
// caller defers and rechecks; freshness alone never silently spends the mail.
func (e *Engine) contactLooksLive(l *core.Agent) bool {
	if l == nil || l.Status != core.StatusActive {
		return false
	}
	last := e.lastEvidenceOf(l)
	if last.IsZero() || time.Since(last) >= contactActivityGrace {
		return false
	}
	if ended := e.turnEnded[l.ID]; !ended.IsZero() && !ended.Before(last) {
		return false
	}
	return true
}

type contactLinkPlan struct {
	local, knownHost               bool
	host, surface, harness, thread string
}

// contactNotice runs on the writer and snapshots only daemon-controlled
// metadata. Filesystem lookup for an app deep link happens later, off-loop.
func (e *Engine) contactNotice(c *core.ContactEscalation) (HumanNotice, contactLinkPlan) {
	name, sender, kind := e.agentName(c.Recipient), "an agent", "message"
	if m := e.state.Messages[c.OldestSerial]; m != nil {
		sender, kind = e.agentName(m.From), m.Type
	}
	if name == "" {
		name = c.Recipient
	}
	if sender == "" {
		sender = "an agent"
	}
	host, harness := "this machine", "its original harness"
	plan := contactLinkPlan{}
	if l := e.state.Agents[c.Recipient]; l != nil {
		plan.local = e.ownsHost(l)
		plan.surface, plan.thread = surfaceOf(l), threadIDOf(l)
		if l.Agent != nil {
			plan.harness = l.Agent.Harness
			if l.Agent.Harness != "" {
				harness = l.Agent.Harness
			}
			if l.Agent.Host != "" {
				host = l.Agent.Host
			}
			if l.Agent.HostID != "" {
				host = l.Agent.HostID
			}
		}
	}
	if plan.local {
		plan.host, plan.knownHost = e.HostID(), true
	}
	n := HumanNotice{
		Serial: c.Serial, Type: "contact", Node: e.state.NodeID,
		Contact: &humanask.Contact{
			Recipient: name, Sender: sender, Kind: kind,
			Message: c.OldestSerial, Count: c.Count,
			OpenHint: "open " + name + " in " + harness + " on " + host,
		},
	}
	return n, plan
}

func enrichContactURL(n *HumanNotice, plan contactLinkPlan) {
	if n.Contact == nil || !plan.local || !plan.knownHost || plan.thread == "" {
		return
	}
	surface := harnessenv.AppFor(plan.surface, plan.harness, plan.thread)
	argv := harnessenv.OpenArgv(surface, plan.thread)
	if len(argv) >= 2 && argv[0] == "/usr/bin/open" {
		n.Contact.OpenURL = argv[len(argv)-1]
		n.Contact.OpenHost = plan.host
	}
}

// scheduleContactDelivery runs on the writer. Attempts are derived and may be
// retried after a restart; only a posted receipt changes the replayable state.
func (e *Engine) scheduleContactDelivery(serial uint64, now time.Time) {
	c := e.state.Contacts[serial]
	if c == nil || !c.NotifiedAt.IsZero() || !c.ResolvedAt.IsZero() {
		return
	}
	if at := e.contactAttempts[serial]; !at.IsZero() && now.Sub(at) < contactRetryInterval {
		return
	}
	if e.contactAttempts == nil {
		e.contactAttempts = map[uint64]time.Time{}
	}
	e.contactAttempts[serial] = now
	go func() {
		// A recipient that reads or answers immediately needs no human to open
		// it. Snapshot after the settle window so a burst's final count reaches
		// the one alert, rather than the first message's count.
		timer := time.NewTimer(contactSettleDelay)
		defer timer.Stop()
		<-timer.C
		n, plan, ok := e.contactSnapshot(serial)
		if !ok {
			return
		}
		enrichContactURL(&n, plan)
		if e.contactStillOutstanding(serial) {
			e.deliverContactNotice(n)
		}
	}()
}

func (e *Engine) contactSnapshot(serial uint64) (HumanNotice, contactLinkPlan, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var n HumanNotice
	var plan contactLinkPlan
	res, err := e.query(ctx, func() core.Result {
		c := e.state.Contacts[serial]
		if c == nil || !c.NotifiedAt.IsZero() || !c.ResolvedAt.IsZero() {
			return nil
		}
		n, plan = e.contactNotice(c)
		return core.Result{"open": true}
	})
	return n, plan, err == nil && res["open"] == true
}

func (e *Engine) contactStillOutstanding(serial uint64) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := e.query(ctx, func() core.Result {
		c := e.state.Contacts[serial]
		return core.Result{"open": c != nil && c.NotifiedAt.IsZero() && c.ResolvedAt.IsZero()}
	})
	return err == nil && res["open"] == true
}

func (e *Engine) deliverContactNotice(n HumanNotice) {
	e.setContactIssued(n.Serial, true)
	if e.enqueueHumanNotice(n) > 0 {
		return
	}
	available, ask := e.localHumanNotifier()
	if !available() {
		e.setContactIssued(n.Serial, false)
		slog.Warn("contact alert remains outstanding: no human notification route", "contact", n.Serial)
		return
	}
	e.askHumanDesktop(n, ask)
}

func (e *Engine) retryContactDelivery(now time.Time) {
	ids := make([]uint64, 0, len(e.state.Contacts))
	for id := range e.state.Contacts {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		if c := e.state.Contacts[id]; c != nil && c.NotifiedAt.IsZero() && c.ResolvedAt.IsZero() {
			e.scheduleContactDelivery(id, now)
		} else {
			delete(e.contactAttempts, id)
		}
	}
}

// A recipient's actual read/answer supersedes the need for a person to open
// that session. Runs after the source op and its events were ledgered, never
// inside publish (which would append a newer event before the older one).
func (e *Engine) resolveContactsAfter(evs []core.Event, now time.Time) {
	seen := map[uint64]bool{}
	for _, ev := range evs {
		if !strings.HasPrefix(ev.Type, "message.") {
			continue
		}
		serial, _ := ev.Data["msg_serial"].(uint64)
		m := e.state.Messages[serial]
		if m == nil || m.ContactEscalatedAt == 0 || seen[m.ContactEscalatedAt] {
			continue
		}
		id := m.ContactEscalatedAt
		seen[id] = true
		e.resolveContactIfClear(id, now)
	}
}

func (e *Engine) resolveContactIfClear(id uint64, now time.Time) {
	c := e.state.Contacts[id]
	if c == nil || !c.ResolvedAt.IsZero() {
		return
	}
	for _, item := range e.state.Messages {
		if item.ContactEscalatedAt == id && item.State == core.MsgStatePending {
			return
		}
	}
	op := &core.Op{Kind: core.OpContactResolved, ContactSerial: id}
	if err := e.state.Admit(op); err != nil {
		return
	}
	if _, err := e.applyAndLedger(op, now); err != nil {
		slog.Error("could not record resolved contact alert", "contact", id, "err", err)
	}
}

// contactPostedDecision is the only step that settles an outstanding contact.
// A relay queue or notification attempt does not, even when it returned OK.
func (e *Engine) contactPostedDecision(serial uint64, now time.Time) {
	op := &core.Op{Kind: core.OpContactNotified, ContactSerial: serial}
	if err := e.state.Admit(op); err != nil {
		return
	}
	if _, err := e.applyAndLedger(op, now); err != nil {
		slog.Error("could not ledger a posted contact receipt", "contact", serial, "err", err)
	}
}

func (e *Engine) noteContactPosted(serial uint64) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = e.query(ctx, func() core.Result {
		e.contactPostedDecision(serial, time.Now())
		return nil
	})
}

// hasWakeRoute asks for reachability without consuming a cooldown or queuing
// another command. A wakeFor miss may mean an in-flight/queued wake, not an
// unreachable recipient; only a missing route can justify escalation.
func (e *Engine) hasWakeRoute(l *core.Agent) bool {
	e.wakers.mu.Lock()
	defer e.wakers.mu.Unlock()
	_, _, ok := e.wakeRoute(l)
	return ok
}

func (e *Engine) deferContactForLiveTurn(l *core.Agent) bool {
	return !e.hasWakeRoute(l) && e.contactLooksLive(l)
}

func (e *Engine) contactRouteMissingAfterRetry(l *core.Agent) bool {
	return !e.hasWakeRoute(l) &&
		(surfaceOf(l) != harnessenv.ClaudeDesktop || threadIDOf(l) == "")
}

func (e *Engine) contactRouteMissingNow(l *core.Agent) bool {
	return !e.hasWakeRoute(l) && !e.socketMayHaveAppeared(l)
}

// escalateContact runs only on the writer, after wake route classification.
// The system op contains a source serial, never a participant's text.
func (e *Engine) escalateContact(agent string) {
	l := e.state.Agents[agent]
	if l == nil || l.Retired() || agent == e.humanIdentityLocked() ||
		e.hasWakeRoute(l) || e.recentlyInTouch(l) || e.contactLooksLive(l) {
		return
	}
	for _, m := range e.state.Inbox(agent) {
		if m.State != core.MsgStatePending || m.ContactEscalatedAt != 0 ||
			(m.Type != core.MsgQuestion && m.Type != core.MsgRequest && m.Type != core.MsgHandoff) {
			continue
		}
		op := &core.Op{Kind: core.OpContactEscalate, MsgSerial: m.Serial}
		if err := e.state.Admit(op); err != nil {
			slog.Debug("contact escalation is no longer eligible", "agent", agent, "msg", m.Serial, "err", err)
			continue
		}
		if _, err := e.applyAndLedger(op, time.Now()); err != nil {
			slog.Error("could not record contact escalation", "agent", agent, "msg", m.Serial, "err", err)
		}
	}
}

// An app reopening is still a route while it is pending. Only an observed
// failure/no mapping returns here; a deferred open never escalates by itself.
func (e *Engine) contactAfterAppFailure(agent string, created uint64) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = e.query(ctx, func() core.Result {
		l := e.state.Agents[agent]
		if l != nil && l.CreatedSerial == created {
			e.escalateContact(agent)
		}
		return nil
	})
}

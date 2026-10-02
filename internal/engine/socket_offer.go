package engine

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// A socket write has no receipt from the harness. Keep what was offered,
// without spending presentation, until BOTH the write succeeds and this
// session shows new-turn evidence after the offer. Ongoing-turn tool calls
// cannot confirm mail a permissions gate may have held. Stop alone is no such
// evidence: a held peer message must still have its hook fallback.
// Ephemeral and bounded to one offer per agent; no mailbox state is changed.
type socketOffer struct {
	id, session         string
	at                  time.Time
	written             bool
	canConfirm          bool
	mail, announcements []string
	notices             []string
}

func (e *Engine) beginSocketOffer(l *core.Agent, session string) core.Result {
	now := time.Now()
	text := e.currentWakeDigest(l)
	if text == "" {
		return core.Result{"digest": ""}
	}
	if e.socketOffers == nil {
		e.socketOffers = map[string]socketOffer{}
	}
	e.nextSocketOffer++
	id := strconv.FormatUint(e.nextSocketOffer, 10)
	_, announcements := e.dueAnnouncements(l.ID, now)
	_, notices := e.dueNoticeLines(l.ID, now)
	e.socketOffers[l.ID] = socketOffer{
		id: id, session: session, at: now,
		canConfirm: e.turnEnded[l.ID].After(e.seen[l.ID]),
		mail:       e.wakeKeys(l.ID, now), announcements: announcements, notices: notices,
	}
	return core.Result{"digest": text, "offer": id}
}

func (e *Engine) settleSocketOffer(agent, session, id string, written bool) {
	o, ok := e.socketOffers[agent]
	if !ok || o.id != id || o.session != session {
		return
	}
	l := e.state.Agents[agent]
	if !written || l == nil || !l.SessionIsCurrent(session) {
		delete(e.socketOffers, agent)
		return
	}
	o.written = true
	e.socketOffers[agent] = o
	if e.seen[agent].After(o.at) {
		e.confirmSocketOffer(l, e.seen[agent])
	}
}

// Called only by model activity and starting hooks, never event observers,
// finishing hooks, lease probes or socket bookkeeping itself.
func (e *Engine) confirmSocketOffer(l *core.Agent, now time.Time) {
	o, ok := e.socketOffers[l.ID]
	if !ok || !o.written || !o.canConfirm || !now.After(o.at) || !l.SessionIsCurrent(o.session) {
		return
	}
	e.markWoken(o.mail, o.at)
	e.markAnnounced(o.announcements, o.at)
	e.markNoticePresentation(o.notices, o.at)
	delete(e.socketOffers, l.ID)
}

// A starting lifecycle event explicitly distinguishes a new turn from
// tool traffic in a turn that was already running when mail was offered.
func (e *Engine) noteSocketTurnStart(l *core.Agent, now time.Time) {
	o, ok := e.socketOffers[l.ID]
	if ok && now.After(o.at) && l.SessionIsCurrent(o.session) {
		o.canConfirm = true
		e.socketOffers[l.ID] = o
	}
	e.confirmSocketOffer(l, now)
}

// Delivery timing is separate from the notices themselves. check_in and
// read_mail still return every owed notice regardless of this throttle.
func (e *Engine) dueNoticeLines(agent string, now time.Time) (lines, keys []string) {
	live := map[string]bool{}
	for _, n := range e.takeNotices(agent) {
		key := agent + "\x00" + strconv.FormatUint(n.Serial, 10)
		live[key] = true
		if at, ok := e.noticePresented[key]; ok && now.Sub(at) < AnnounceRetry {
			continue
		}
		lines, keys = append(lines, n.Text), append(keys, key)
	}
	for key := range e.noticePresented {
		if len(key) > len(agent) && key[:len(agent)+1] == agent+"\x00" && !live[key] {
			delete(e.noticePresented, key)
		}
	}
	return lines, keys
}

func (e *Engine) markNoticePresentation(keys []string, now time.Time) {
	if e.noticePresented == nil {
		e.noticePresented = map[string]time.Time{}
	}
	for _, key := range keys {
		e.noticePresented[key] = now
	}
}

func (e *Engine) forgetPresentation(agent string) {
	delete(e.socketOffers, agent)
	for key := range e.noticePresented {
		if strings.HasPrefix(key, agent+"\x00") {
			delete(e.noticePresented, key)
		}
	}
}

// Verdicts bypass the operator's situational-notice switch, but share the
// presentation cadence. The authenticated raw queue remains complete.
func (e *Engine) dueBlockingNotices(agent string, now time.Time) int {
	n := 0
	for _, x := range e.takeNotices(agent) {
		key := agent + "\x00" + strconv.FormatUint(x.Serial, 10)
		at, shown := e.noticePresented[key]
		if x.Blocking && (!shown || now.Sub(at) >= AnnounceRetry) {
			n++
		}
	}
	return n
}

// SocketOfferFor is the bridge's additive write-time presentation handshake.
// Authentication and session binding are checked on the writer loop. A plain
// digest read remains non-consuming for dormant pre-upgrade bridges.
func (e *Engine) SocketOfferFor(ctx context.Context, token, session, id string, written bool) (core.Result, error) {
	return e.query(ctx, func() core.Result {
		l := e.state.AgentByToken(token)
		if l == nil {
			return core.Result{"error": core.ErrBadToken}
		}
		if !l.SessionIsCurrent(session) {
			return core.Result{"digest": ""}
		}
		if id != "" {
			e.settleSocketOffer(l.ID, session, id, written)
			return core.Result{"digest": ""}
		}
		return e.beginSocketOffer(l, session)
	})
}

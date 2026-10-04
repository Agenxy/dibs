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
	work                []socketWorkKey
	backoff             socketBackoff
}

func (e *Engine) beginSocketOffer(l *core.Agent, session string) core.Result {
	return e.beginSocketOffers([]*core.Agent{l}, session)
}

// One reservation for the whole session, including daemon fallback writers.
// A bridge holding several tokens supplies them together, so aggregation and
// reservation are atomic without granting one token another mailbox's text.
func (e *Engine) beginSocketOffers(agents []*core.Agent, session string) core.Result {
	now := time.Now()
	l := agents[0]
	key := socketSessionKey(l)
	if old, ok := e.socketEpochs[key]; ok {
		// A writer disappearing before settlement cannot wedge the route.
		// Written epochs have no timer: a held peer is not a reason to repeat.
		if old.written || now.Sub(old.at) < 15*time.Second {
			return core.Result{"digest": ""}
		}
		delete(e.socketEpochs, key)
	}
	var texts []string
	for _, a := range agents {
		if text := e.socketDigest(a, now); text != "" {
			texts = append(texts, text)
		}
	}
	if len(texts) == 0 {
		return core.Result{"digest": ""}
	}
	if e.socketOffers == nil {
		e.socketOffers = map[string]socketOffer{}
	}
	e.nextSocketOffer++
	id := strconv.FormatUint(e.nextSocketOffer, 10)
	if e.socketEpochs == nil {
		e.socketEpochs = map[string]socketEpoch{}
	}
	e.socketEpochs[key] = socketEpoch{id: id, at: now}
	for _, a := range agents {
		_, announcements := e.dueAnnouncements(a.ID, now)
		_, notices := e.dueNoticeLines(a.ID, now)
		_, work := e.dueSocketWaits(a, now)
		e.socketOffers[a.ID] = socketOffer{
			id: id, session: session, at: now,
			canConfirm: e.socketLifecycle(a, now) == "idle",
			mail:       e.wakeKeys(a.ID, now), announcements: announcements, notices: notices,
			work: work, backoff: e.socketBackoff[a.ID],
		}
	}
	e.logSocketOffer(l, now, id)
	return core.Result{"digest": strings.Join(texts, "\n"), "offer": id}
}

func (e *Engine) settleSocketOffer(agent, session, id string, written bool) {
	o, ok := e.socketOffers[agent]
	if !ok || o.id != id || o.session != session || o.written {
		return
	}
	l := e.state.Agents[agent]
	if !written || l == nil || !l.SessionIsCurrent(session) {
		if l != nil && e.socketEpochs[socketSessionKey(l)].id == id {
			delete(e.socketEpochs, socketSessionKey(l))
		}
		for who, other := range e.socketOffers {
			if other.id == id {
				delete(e.socketOffers, who)
			}
		}
		return
	}
	key := socketSessionKey(l)
	if epoch := e.socketEpochs[key]; epoch.id == id {
		epoch.written = true
		e.socketEpochs[key] = epoch
	}
	for who, other := range e.socketOffers {
		if other.id != id {
			continue
		}
		e.settleSocketParticipant(who, key, other)
	}
}

func (e *Engine) settleSocketParticipant(who, key string, offer socketOffer) {
	row := e.state.Agents[who]
	if row == nil || !row.SessionIsCurrent(offer.session) || socketSessionKey(row) != key {
		delete(e.socketOffers, who)
		return
	}
	offer.written = true
	e.socketOffers[who] = offer
	e.markSocketWork(row, offer.work, offer.backoff, time.Now(), true)
	if e.seen[who].After(offer.at) {
		e.confirmSocketOffer(row, e.seen[who])
	}
}

// Called only by model activity and starting hooks, never event observers,
// finishing hooks, lease probes or socket bookkeeping itself.
func (e *Engine) confirmSocketOffer(l *core.Agent, now time.Time) {
	key := socketSessionKey(l)
	for who, o := range e.socketOffers {
		row := e.state.Agents[who]
		if row == nil || socketSessionKey(row) != key || !row.SessionIsCurrent(o.session) ||
			!o.written || !o.canConfirm || !now.After(o.at) {
			continue
		}
		e.markWoken(o.mail, o.at)
		e.markAnnounced(o.announcements, o.at)
		e.markNoticePresentation(o.notices, o.at)
		delete(e.socketOffers, who)
	}
}

// A starting lifecycle event explicitly distinguishes a new turn from
// tool traffic in a turn that was already running when mail was offered.
func (e *Engine) noteSocketTurnStart(l *core.Agent, now time.Time) {
	key := socketSessionKey(l)
	for who, o := range e.socketOffers {
		row := e.state.Agents[who]
		if row != nil && socketSessionKey(row) == key && now.After(o.at) && row.SessionIsCurrent(o.session) {
			o.canConfirm = true
			e.socketOffers[who] = o
		}
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
		if n.Delivered {
			continue
		}
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

// Called only after a delivering Stop has put these exact notices in model
// text. A socket write has no receipt, and unsupported hooks deliver nothing.
func (e *Engine) markInformationalNoticesDelivered(event, agent string, keys []string) {
	if event != "Stop" && event != "SubagentStop" {
		return
	}
	shown := make(map[string]bool, len(keys))
	for _, key := range keys {
		shown[key] = true
	}
	for i := range e.notices[agent] {
		n := &e.notices[agent][i]
		if !n.Blocking && shown[agent+"\x00"+strconv.FormatUint(n.Serial, 10)] {
			n.Delivered = true
		}
	}
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
	return e.SocketOffersFor(ctx, []string{token}, session, id, written)
}

// SocketOffersFor atomically reserves a session's owned mailboxes. Each token
// authenticates its own text; unrelated hosts or current sessions never join.
func (e *Engine) SocketOffersFor(
	ctx context.Context, tokens []string, session, id string, written bool,
) (core.Result, error) {
	res, err := e.query(ctx, func() core.Result {
		agents := e.socketAuthorizedParticipants(tokens, session)
		if len(agents) == 0 {
			if len(tokens) == 1 && e.state.AgentByToken(tokens[0]) == nil {
				return core.Result{"error": core.ErrBadToken}
			}
			return core.Result{"digest": ""}
		}
		if id != "" {
			for _, l := range agents {
				e.settleSocketOffer(l.ID, session, id, written)
			}
			return core.Result{"digest": ""}
		}
		return e.beginSocketOffers(agents, session)
	})
	if err != nil {
		return nil, err
	}
	if refused, ok := res["error"].(error); ok {
		return nil, refused
	}
	return res, nil
}

func (e *Engine) socketAuthorizedParticipants(tokens []string, session string) []*core.Agent {
	var agents []*core.Agent
	seen := map[string]bool{}
	for _, token := range tokens {
		l := e.state.AgentByToken(token)
		if l == nil || !l.SessionIsCurrent(session) {
			continue // a moved/revoked mailbox cannot suppress its live peers
		}
		if len(agents) > 0 && socketSessionKey(l) != socketSessionKey(agents[0]) {
			continue // never aggregate another host or current session
		}
		if !seen[l.ID] {
			seen[l.ID] = true
			agents = append(agents, l)
		}
	}
	return agents
}

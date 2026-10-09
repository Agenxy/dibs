// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"log/slog"
	"strconv"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// Lifecycle and delivery epochs are observations, not coordination state.
// Neither survives replay. A lost Stop becomes unknown after bounded silence,
// never busy inferred from a lease or idle inferred from silence.
type socketTurn struct {
	state string
	at    time.Time
}

type socketEpoch struct {
	id      string
	at      time.Time
	written bool
}

func socketSessionKey(l *core.Agent) string {
	session := l.CurrentSession
	if session == "" {
		session = l.SessionID
	}
	if session == "" {
		// No evidence two unbound rows share a harness. Fence the fallback
		// to the incarnation rather than inheriting a reclaimed row's state.
		session = l.ID + ":" + strconv.FormatUint(l.CreatedSerial, 10)
	}
	host := ""
	if l.Agent != nil {
		host = l.Agent.HostID
		if host == "" {
			host = l.Agent.Host // pre-host-ID rows retain their legacy evidence
		}
	}
	return host + "\x00" + session
}

func (e *Engine) noteSocketBusy(l *core.Agent, now time.Time) {
	if e.socketTurns == nil {
		e.socketTurns = map[string]socketTurn{}
	}
	key := socketSessionKey(l)
	e.socketTurns[key] = socketTurn{state: "busy", at: now}
	// A new turn rearms the session for its next idle epoch. Outstanding
	// offers still keep their held-peer hook fallback until confirmed.
	delete(e.socketEpochs, key)
	delete(e.socketFailures, key)
}

func (e *Engine) noteSocketIdle(l *core.Agent, now time.Time) {
	if e.socketTurns == nil {
		e.socketTurns = map[string]socketTurn{}
	}
	e.socketTurns[socketSessionKey(l)] = socketTurn{state: "idle", at: now}
}

// Authenticated calls can come from outside the session's turn, and a Stop can
// be lost. Silence cannot prove idle, but must not suppress wakes forever.
const socketBusyCeiling = 30 * time.Minute

// Fresh busy observations suppress sockets; stale ones become unknown. Only
// unobserved lifecycle has the route contact/boot grace before recovery.
func (e *Engine) socketLifecycle(l *core.Agent, now time.Time) string {
	if turn := e.socketTurns[socketSessionKey(l)]; turn.state != "" {
		if turn.state == "busy" && now.Sub(turn.at) >= socketBusyCeiling {
			return "unknown"
		}
		return turn.state
	}
	if last := e.lastEvidenceOf(l); !last.IsZero() && now.Sub(last) < e.recencyWindow(l) {
		return "grace"
	}
	return "unknown"
}

func waitingDeclaration(l *core.Agent) bool {
	for _, s := range l.Slots {
		if s.Waiting != "" {
			return true
		}
	}
	return false
}

// The canonical event classifier and typed notices are the policy inputs.
// No rendered-text matching and no state table that can confuse an earlier
// approval with a later DONE or a flagged review of already completed work.
func (e *Engine) actionableSocketMail(l *core.Agent, now time.Time, fresh bool) bool {
	if l.Retired() || e.WakePolicy() == WakeNone {
		return false
	}
	// A required acknowledgment is an outstanding obligation. Use the same
	// due-announcement cadence as the delivering hook, without spending it.
	if !fresh && len(e.state.Unacked(l.ID)) > 0 {
		return true
	}
	if due, _ := e.dueAnnouncements(l.ID, now); len(due) > 0 {
		return true
	}
	wanted := map[string]bool{}
	if fresh {
		for _, key := range e.wakeKeys(l.ID, now) {
			wanted[key] = true
		}
	}
	for _, m := range e.state.Inbox(l.ID) {
		if m.State != core.MsgStatePending && m.State != core.MsgStateDelivered {
			continue
		}
		key := l.ID + "\x00" + strconv.FormatUint(m.Serial, 10)
		if e.notifyPresented(l.ID, m) {
			continue // once, even on reconnect's fresh=false path
		}
		if fresh && !wanted[key] {
			continue
		}
		if e.socketActionableMessage(m) {
			return true
		}
	}
	return e.socketActionableNotices(l, now, fresh)
}

func (e *Engine) socketActionableNotices(l *core.Agent, now time.Time, fresh bool) bool {
	for _, n := range e.takeNotices(l.ID) {
		key := l.ID + "\x00" + strconv.FormatUint(n.Serial, 10)
		if at, shown := e.noticePresented[key]; fresh && (n.Delivered || (shown && now.Sub(at) < AnnounceRetry)) {
			continue
		}
		if wakeCauseAllowed(e.WakePolicy(), false, socketActionableNotice(n, l, e.state.Messages[n.Msg])) {
			return true
		}
	}
	return false
}

func (e *Engine) socketActionableMessage(m *core.Message) bool {
	// Authored mail deserves delivery even when it asks for no reply. Only
	// generated notices use the narrower decision rule below.
	return wakeCauseAllowed(e.WakePolicy(), true, core.Blocking("message.sent", m.Type))
}

// Raw FYIs stay in the mailbox until ack. A ledgered mailbox presentation or
// a confirmed hook/socket digest is enough to spend their one wake; neither
// an unconfirmed socket write nor command execution is a read receipt.
func (e *Engine) notifyPresented(agent string, m *core.Message) bool {
	if m.Type != core.MsgNotify {
		return false
	}
	_, shown := e.wokeFor[agent+"\x00"+strconv.FormatUint(m.Serial, 10)]
	// A later adoption must not inherit the prior recipient's presentation.
	deliveredHere := m.State == core.MsgStateDelivered && m.DeliveredAt >= m.AdoptedAt
	return deliveredHere || shown
}

// One phase rule for authored mail and typed generated notices, used by
// sockets, Stop hooks and the operator's command route. Explicit opt-outs
// belong to the operator; default delivery includes authored FYIs.
func wakeCauseAllowed(phase WakePhase, authored, blocking bool) bool {
	return phase != WakeNone && (blocking || (authored && phase == WakeAll))
}

func socketActionableNotice(n notice, l *core.Agent, request *core.Message) bool {
	// Queue acceptance and position changes report scheduling, not a new
	// decision the sender must act on. Keep them for the next real delivery.
	if n.Kind == "message.queued" || n.Kind == "message.queue_changed" {
		return false
	}
	// Ordinary approval accepts work; grant/adoption approval performs an
	// effect the requester awaits. Read the typed request, never the prose.
	if n.Kind == "message.approved" && (request == nil || request.Type != core.MsgRequest ||
		(request.Grant == "" && request.Adopt == "")) {
		return false
	}
	return n.Blocking && (n.Kind != "message.done" || waitingDeclaration(l))
}

func (e *Engine) socketOwnsDelivery(l *core.Agent) bool {
	if own, _ := e.SelfWaking(l.ID); own {
		return true
	}
	now := time.Now()
	return e.mightReachOverSocket(l) && (!e.socketCanPresent(l, now) || !e.socketDaemonReady(l, now))
}

func (e *Engine) socketHasCause(l *core.Agent, now time.Time) bool {
	key := socketSessionKey(l)
	for _, id := range sortedAgentIDs(e.state) {
		other := e.state.Agents[id]
		if other.Retired() || socketSessionKey(other) != key {
			continue
		}
		mail := e.WakePolicy() != WakeNone && e.actionableSocketMail(other, now, true)
		if mail {
			return true
		}
	}
	return false
}

func (e *Engine) socketParticipants(l *core.Agent) []*core.Agent {
	key := socketSessionKey(l)
	var rows []*core.Agent
	for _, id := range sortedAgentIDs(e.state) {
		row := e.state.Agents[id]
		if !row.Retired() && socketSessionKey(row) == key {
			rows = append(rows, row)
		}
	}
	return rows
}

// Both writers enter here immediately before a write. Hooks intentionally use
// the full formatter directly. A successful write is coalesced until a real
// turn begins, even when a held peer generated no turn at all.
func (e *Engine) socketDigest(l *core.Agent, now time.Time) string {
	if !e.socketCanPresent(l, now) {
		return ""
	}
	text := e.currentWakeDigest(l)
	return text
}

func (e *Engine) socketCanPresent(l *core.Agent, now time.Time) bool {
	state := e.socketLifecycle(l, now)
	if state == "busy" || state == "grace" || !e.socketHasCause(l, now) {
		return false
	}
	key := socketSessionKey(l)
	if epoch, ok := e.socketEpochs[key]; ok && epoch.written {
		return false
	}
	return true
}

// SocketReadyEvent is a derived readiness hint on the existing subscription,
// with no ledger position. It is never appended to the ring or replayed. A
// reconnect reads current outstanding mail if the hint was missed.
const SocketReadyEvent = "socket.ready"

func (e *Engine) signalSocketReady(l *core.Agent) {
	ev := core.Event{Type: SocketReadyEvent, To: l.ID}
	for ch := range e.streams {
		select {
		case ch <- ev:
		default: // reconnect reads outstanding mail; this is not a ledger cursor
		}
	}
}

func (e *Engine) pruneSocketState() {
	live := map[string]bool{}
	for _, id := range sortedAgentIDs(e.state) {
		l := e.state.Agents[id]
		if l.Retired() {
			continue
		}
		live[socketSessionKey(l)] = true

	}
	for key := range e.socketTurns {
		if !live[key] {
			delete(e.socketTurns, key)
		}
	}
	for key := range e.socketEpochs {
		if !live[key] {
			delete(e.socketEpochs, key)
		}
	}
	for key := range e.socketFailures {
		if !live[key] {
			delete(e.socketFailures, key)
		}
	}
}

func (e *Engine) wakeSocketReady(l *core.Agent) {
	now := time.Now()
	if !e.socketCanPresent(l, now) || !e.socketDaemonReady(l, now) {
		return
	}
	plan, ok := e.wakeFor(l, "notice", core.Event{Type: SocketReadyEvent, To: l.ID})
	if !ok {
		return
	}
	cause := e.failedDeliveryKeys(l.ID)
	agent, stamp := l.ID, e.wakeStamp(l.ID)
	go func() {
		defer e.wakeExited(agent, plan.thread)
		n := e.noteWakeAttempt(agent)
		if !e.runWakeAndReport(plan, agent) {
			e.releaseWake(agent, stamp)
			if n < 2 {
				e.armFailedWake(agent, cause, plan.cooldown)
			}
		}
	}()
}

func (e *Engine) logSocketOffer(l *core.Agent, now time.Time, id string) {
	slog.Info("socket wake offered", "agent", l.ID, "session_id", l.CurrentSession,
		"lifecycle", e.socketLifecycle(l, now), "offer", id)
}

func (e *Engine) wakeAnnouncementEvent(ev core.Event) {
	if ev.Type != "agent.announce" {
		return
	}
	for _, id := range sortedAgentIDs(e.state) {
		l := e.state.Agents[id]
		if !l.Retired() && len(e.state.Unacked(id)) > 0 {
			e.maybeWake(core.Event{Type: "announcement.pending", To: id})
		}
	}
}

// A finishing hook is a lifecycle event. If it delivered no model digest,
// the existing socket writer may offer outstanding mail, once in this epoch.
func (e *Engine) socketIdleEvent(l *core.Agent, event string) {
	if !isStopEvent(event) || !e.socketCanPresent(l, time.Now()) {
		return
	}
	if own, _ := e.SelfWaking(l.ID); own {
		e.signalSocketReady(l)
	} else if e.mightReachOverSocket(l) {
		e.wakeSocketReady(l)
	}
}

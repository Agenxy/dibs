package engine

import (
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/wakeexec"
)

// Lifecycle and delivery epochs are observations, not coordination state.
// Neither survives replay. A lost Stop therefore becomes unknown, never busy
// inferred from a lease or idle inferred from silence.
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

// Actual lifecycle observations never time out. Only unknown has a grace,
// using the existing route contact window (also used for boot/registration).
func (e *Engine) socketLifecycle(l *core.Agent, now time.Time) string {
	if turn := e.socketTurns[socketSessionKey(l)]; turn.state != "" {
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
	if l.Retired() {
		return false
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
		if socketActionableNotice(n, l) {
			return true
		}
	}
	return false
}

func (e *Engine) socketActionableMessage(m *core.Message) bool {
	return core.Blocking("message.sent", m.Type) || (m.Type == core.MsgNotify && e.isTheHuman(m.From))
}

func socketActionableNotice(n notice, l *core.Agent) bool {
	return n.Blocking && (n.Kind != "message.done" || waitingDeclaration(l))
}

func (e *Engine) socketOwnsDelivery(l *core.Agent) bool {
	if own, _ := e.SelfWaking(l.ID); own {
		return true
	}
	now := time.Now()
	return e.mightReachOverSocket(l) && (e.socketDigest(l, now) == "" || !e.socketDaemonReady(l, now))
}

func (e *Engine) socketHasCause(l *core.Agent, now time.Time) bool {
	key := socketSessionKey(l)
	for _, id := range sortedAgentIDs(e.state) {
		other := e.state.Agents[id]
		if other.Retired() || socketSessionKey(other) != key {
			continue
		}
		mail := e.WakePolicy() != WakeNone && e.actionableSocketMail(other, now, true)
		if mail || e.socketWorkDigest(other, now) != "" {
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
	state := e.socketLifecycle(l, now)
	if state == "busy" || state == "grace" || !e.socketHasCause(l, now) {
		return ""
	}
	key := socketSessionKey(l)
	if epoch, ok := e.socketEpochs[key]; ok && epoch.written {
		return ""
	}
	text := e.currentWakeDigest(l)
	if work := e.socketWorkDigest(l, now); work != "" {
		if text != "" {
			text += "\n"
		}
		text += work
	}
	return text
}

// SocketReadyEvent is a derived readiness hint on the existing subscription,
// with no ledger position. It is never appended to the ring or replayed. A
// missed hint is rebuilt by this tick from current mail and declarations.
const SocketReadyEvent = "socket.ready"

func (e *Engine) signalSocketReady(l *core.Agent) {
	ev := core.Event{Type: SocketReadyEvent, To: l.ID}
	for ch := range e.streams {
		select {
		case ch <- ev:
		default: // derived hint; the next tick reconstructs it, no cursor lost
		}
	}
}

func (e *Engine) socketReadyTick(now time.Time) {
	live := map[string]bool{}
	for _, id := range sortedAgentIDs(e.state) {
		l := e.state.Agents[id]
		if l.Retired() {
			continue
		}
		live[socketSessionKey(l)] = true
		e.observeSocketWaits(l, now)
		if e.socketDigest(l, now) == "" {
			continue
		}
		if own, _ := e.SelfWaking(id); own {
			e.signalSocketReady(l)
		} else if e.mightReachOverSocket(l) {
			e.wakeSocketReady(l)
		}
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
	for key := range e.socketWaits {
		id, _, _ := strings.Cut(key, "\x00")
		if e.state.Agents[id].Retired() {
			delete(e.socketWaits, key)
		}
	}
}

func (e *Engine) wakeSocketReady(l *core.Agent) {
	if !e.socketDaemonReady(l, time.Now()) {
		return
	}
	kind := "notice"
	if e.socketWorkDigest(l, time.Now()) != "" {
		kind = wakeexec.KindRecheck
	}
	plan, ok := e.wakeFor(l, kind, core.Event{Type: SocketReadyEvent, To: l.ID})
	if !ok {
		return
	}
	go func() {
		defer e.wakeExited(l.ID, plan.thread)
		e.runWakeAndReport(plan, plan.agent)
	}()
}

func (e *Engine) logSocketOffer(l *core.Agent, now time.Time, id string) {
	slog.Info("socket wake offered", "agent", l.ID, "session_id", l.CurrentSession,
		"lifecycle", e.socketLifecycle(l, now), "offer", id)
}

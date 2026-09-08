package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/engine"
)

// rpcErrFrom maps a core error to a JSON-RPC error, preserving the code/hint.
func rpcErrFrom(err error) *rpcError {
	var ce *core.Error
	if errors.As(err, &ce) {
		return &rpcError{Code: -32000, Message: ce.Msg, Data: map[string]any{"code": ce.Code, "hint": ce.Hint}}
	}
	return &rpcError{Code: -32603, Message: err.Error()}
}

// metaTokenKey names the _meta field that carries an agent token in a
// subscriptions/listen (or resources/read) request, so an inbox subscription can
// be scoped to the caller's agent. The daemon connection is already gated by the
// local secret; this token identifies WHICH agent's mailbox to watch. It is a
// field NAME, not a secret value.
const metaTokenKey = "com.dibs/token" //nolint:gosec // G101: metadata key name, not a credential

// subscriptionParams is the SEP-2575 subscriptions/listen params shape.
type subscriptionParams struct {
	Meta          map[string]any `json:"_meta"`
	Notifications struct {
		ToolsListChanged      bool     `json:"toolsListChanged"`
		PromptsListChanged    bool     `json:"promptsListChanged"`
		ResourcesListChanged  bool     `json:"resourcesListChanged"`
		ResourceSubscriptions []string `json:"resourceSubscriptions"`
	} `json:"notifications"`
}

// sseStream writes JSON-RPC messages as SSE `data:` events on a flushed writer.
type sseStream struct {
	w  http.ResponseWriter
	fl http.Flusher
}

func (s sseStream) send(v any) bool {
	b, err := json.Marshal(v)
	if err != nil {
		return false
	}
	if _, err := fmt.Fprintf(s.w, "data: %s\n\n", b); err != nil {
		return false
	}
	s.fl.Flush()
	return true
}

func (s sseStream) comment() bool {
	if _, err := fmt.Fprint(s.w, ": keepalive\n\n"); err != nil {
		return false
	}
	s.fl.Flush()
	return true
}

// serveSubscription implements SEP-2575 `subscriptions/listen` over HTTP: the
// POST response is a long-lived SSE stream whose first message is
// notifications/subscriptions/acknowledged, followed by
// notifications/resources/updated whenever a subscribed resource changes. This
// is the spec-native push path (the future-proof successor to await_events);
// whether a host surfaces the notification to the model is the host's call, but
// the server half is standards-correct and harmless when unused.
//
// Dibs honors two resource URIs:
//   - dibs://board : any board change (agents/slots/claims). No token needed.
//   - dibs://inbox : mail to the caller's agent. Requires an agent token in
//     _meta[com.dibs/token] so it can be scoped and access-checked.
func (s *Server) serveSubscription(w http.ResponseWriter, r *http.Request, req *rpcRequest) {
	var p subscriptionParams
	_ = json.Unmarshal(req.Params, &p)

	wantBoard := containsStr(p.Notifications.ResourceSubscriptions, "dibs://board")
	wantInbox := containsStr(p.Notifications.ResourceSubscriptions, "dibs://inbox")

	// Resolve the agent (for inbox scoping) and the current serial in one call.
	token := ""
	if wantInbox {
		token, _ = p.Meta[metaTokenKey].(string)
		if token == "" {
			writeRPC(w, http.StatusBadRequest, req.ID, nil, &rpcError{
				Code: -32602, Message: "dibs://inbox subscription requires an agent token in _meta['" + metaTokenKey + "']",
			})
			return
		}
	}
	agentID, since, err := s.eng.SubscribeInfo(r.Context(), token)
	if err != nil {
		writeRPC(w, http.StatusOK, req.ID, nil, rpcErrFrom(err))
		return
	}
	// A reconnecting subscriber says where it left off, and the gap is
	// replayed from the ring rather than lost.
	cursor, resuming := since, false
	if v, ok := p.Meta[SinceMetaKey].(float64); ok && v >= 0 && uint64(v) < since {
		cursor, resuming = uint64(v), true
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeRPC(w, http.StatusInternalServerError, req.ID, nil,
			&rpcError{Code: -32603, Message: "streaming not supported by this server"})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	stream := sseStream{w: w, fl: flusher}

	// SEP-2575: acknowledge first, echoing only the notification types honored.
	honored := map[string]any{}
	if subs := honoredURIs(wantBoard, wantInbox); len(subs) > 0 {
		honored["resourceSubscriptions"] = subs
	}
	// The acknowledgment names the serial the subscription starts from, so a
	// subscriber that drops before its first notification reconnects with a
	// cursor rather than blind. Found by the pre-release review, round
	// thirteen.
	// THE CURSOR, NOT THE PRESENT. A resuming subscriber's acknowledgment
	// carried the current serial before the gap was replayed; a drop between
	// the two made the next reconnect skip the gap for good. Found by the
	// pre-release review, round sixteen.
	stream.send(notification("notifications/subscriptions/acknowledged", map[string]any{
		"notifications": honored, "_meta": map[string]any{SerialMetaKey: cursor},
	}, req.ID))

	// THE GAP, FILTERED FIRST. Subscribe replays the gap through a bounded
	// channel and drops what does not fit, before anything is filtered, so a
	// backlog of unrelated board events crowded out the one notification
	// that would have woken the agent. The events addressed to this agent
	// are replayed here from the ring, filtered, in full; the channel replay
	// below may repeat some, and a repeat coalesces where a loss does not.
	// Found by the pre-release review, round fifteen.
	// SUBSCRIBED FIRST, FROM THE PRESENT. The live channel used to be opened
	// after the gap was replayed, and from the cursor: its catch-up pushed
	// the whole gap into a 256-event buffer that drops when full, so a long
	// gap filled it with history already replayed above and a question sent
	// while the replay was being written landed past the buffer, in neither
	// the replay nor the stream. The channel opens before the replay and
	// starts where SubscribeInfo read the serial, so anything after that
	// point is buffered while the replay runs; the replay covers the gap up
	// to it, and a repeat coalesces where a loss does not. Found by the
	// pre-release review, round thirty-one.
	sub, cancel := s.eng.SubscribeTracked(since)
	defer cancel()
	// Fixed for the lifetime of the stream: 2026-07-28 carries the whole
	// subscription in the listen call, so there is nothing to re-read.
	// THE STANDING IS NOT. The session the stream serves is the one the
	// bridge attaches to every call; whether the agent is still there, and
	// whether the token still names it, is asked as each notification is
	// about to go out. See core.StreamStanding.
	session, _ := p.Meta[SessionMetaKey].(string)
	standing := func() (bool, bool) {
		if token == "" {
			// A stream following the board alone answers to no credential.
			// The check applied to it looked the empty token up, found no
			// agent, and closed the stream on its first notification. Found
			// by the pre-release review, round sixty.
			return true, true
		}
		return s.eng.StreamStanding(r.Context(), token, session)
	}
	// The gap too: a left-behind bridge that reconnected after a daemon
	// restart was handed the mail it had missed, and woke on it. The standing
	// is re-read as the replay writes, because the agent can move mid-replay.
	if _, held := standing(); resuming && wantInbox && held &&
		!s.replayGap(r.Context(), stream, req.ID, agentID, cursor, standing) {
		return
	}
	s.pump(r, stream, sub, since, req.ID, func() (string, bool, bool) {
		return agentID, wantInbox, wantBoard
	}, standing)
}

// standingFunc reports, as a notification is about to go out, whether the
// stream's credential is still live and whether its agent still holds the
// session it serves. A stream whose token is gone ends; one whose agent has
// moved to another session keeps its board notifications and withholds the
// inbox, which is the daemon's own rule for where a wake may go.
type standingFunc func() (live, held bool)

// pump forwards resource-change notifications until the client disconnects.
// wantsFunc reports what a stream should deliver RIGHT NOW: the agent whose
// inbox it follows, and whether it follows the inbox and the board at all.
//
// A function rather than three values captured when the stream opened, because
// the legacy transport lets a client subscribe and unsubscribe while its stream
// is already open. Captured values meant a subscribe AFTER the GET was never
// noticed and an unsubscribe never took effect: the stream kept delivering what
// the client had asked to stop hearing, and stayed silent about what it had
// just asked for.
type wantsFunc func() (agentID string, inbox, board bool)

// last is the serial the stream is complete up to when the pump starts: the
// point the subscription's channel began at. The channel drops when its
// buffer is full rather than stall the writer loop, and says so (Lost); the
// pump then refills from the ring everything after the last serial it
// delivered, so a burst during a slow replay costs repeats, which coalesce,
// and not a question, which did not. Found by the pre-release review, round
// thirty-three.
func (s *Server) pump(r *http.Request, stream sseStream, sub *engine.Subscription, last uint64,
	subID json.RawMessage, wants wantsFunc, standing standingFunc,
) {
	keepalive := time.NewTicker(25 * time.Second)
	defer keepalive.Stop()
	ctx := r.Context()
	// The position starts at the end of `last`: everything at that serial
	// was covered by the caller, so a refill must not repeat it.
	p := &pumpState{
		s: s, ctx: ctx, stream: stream, sub: sub, last: last, lastSub: math.MaxInt, subID: subID,
		wants: wants, standing: standing,
	}
	if !p.refill() { // the replay may have run long enough to overflow already
		return
	}
	for {
		select {
		case <-ctx.Done(): // client closed the stream = cancellation (SEP-2575)
			return
		case <-keepalive.C:
			if !stream.comment() || !p.refill() {
				return
			}
		case ev, open := <-sub.C:
			if !open {
				return
			}
			if !p.refill() || !p.deliver(ev) {
				return
			}
		}
	}
}

// pumpState is one stream's position: the event it is complete up to.
//
// A POSITION, NOT A SERIAL. One op emits several events at one serial,
// numbered by Sub, and the channel drops one event at a time: the first
// event of a prune was delivered, the position moved to its serial, the
// three after it at the same serial were dropped, and the refill asked for
// strictly later serials and recovered none of them. The position is
// (serial, sub), and a refill re-reads the position's serial and skips what
// it already delivered. Found by the pre-release review, round thirty-eight.
type pumpState struct {
	s       *Server
	ctx     context.Context
	stream  sseStream
	sub     *engine.Subscription
	last    uint64
	lastSub int
	subID   json.RawMessage
	wants   wantsFunc
	// standing is asked before each notification goes out; nil for a
	// stream that answers to no credential.
	standing standingFunc
}

// seen reports whether ev is at or before the position.
func (p *pumpState) seen(ev core.Event) bool {
	// A RESYNCED EVENT IS NOT A POSITION. Events rebuilt from the inbox carry
	// no Sub, so every one of them lands on zero, and a stream whose position
	// was already (serial, 0) skipped one as though it had been delivered: an
	// adoption emits agent.updated at sub 0 and the mail event after it, so
	// delivering the first and losing the second left the rebuilt notice
	// looking seen. The mail then sat in the inbox with nothing to announce
	// it, which is the exact loss the resync exists to repair.
	//
	// These describe what is STILL OWED, read from the mail rather than from
	// the ring, so a position cannot prove one was delivered. A repeat
	// coalesces at the subscriber where a loss does not, which is the trade
	// this whole path is built on. Found by the pre-release review, round
	// sixty-nine.
	if resynced, _ := ev.Data["resynced"].(bool); resynced {
		return false
	}
	return ev.Serial < p.last || (ev.Serial == p.last && ev.Sub <= p.lastSub)
}

// deliver forwards one event the stream follows and advances the position.
func (p *pumpState) deliver(ev core.Event) bool {
	agentID, wantInbox, wantBoard := p.wants()
	if uri := matchedURI(ev, agentID, wantInbox, wantBoard); uri != "" {
		if p.standing != nil {
			live, held := p.standing()
			if !live {
				return false // rotated away or retired: whoever holds the new token subscribes afresh
			}
			if !held && uri == "dibs://inbox" {
				uri = "" // the agent is in another session; its mail wakes that one
			}
		}
		if uri != "" && !p.stream.send(resourceUpdated(uri, p.subID, ev)) {
			return false
		}
	}
	if ev.Serial > p.last || (ev.Serial == p.last && ev.Sub > p.lastSub) {
		p.last, p.lastSub = ev.Serial, ev.Sub
	}
	return true
}

// refill replays from the ring everything after the position when the
// channel reports it dropped something; otherwise it does nothing.
func (p *pumpState) refill() bool {
	if !p.sub.Lost() {
		return true
	}
	// From the position's own serial, because the rest of it may be what
	// was dropped; what was delivered is skipped by position. The position
	// is passed as-is: eventsAfter reads inclusively and detects a position
	// the ring has passed, where the old decrement-then-EventsSince turned a
	// position of one into the "whole ring" sentinel. Found by the
	// pre-release review, round sixty-one.
	for _, ev := range p.s.eventsAfter(p.ctx, p.last, p.wants) {
		if p.seen(ev) {
			continue
		}
		if !p.deliver(ev) {
			return false
		}
	}
	return true
}

// eventsAfter is every event after last, from the ring; past the ring, what
// the inbox still owes (ResyncFor), for a stream that follows one.
func (s *Server) eventsAfter(ctx context.Context, pos uint64, wants wantsFunc) []core.Event {
	res, err := s.eng.EventsFrom(ctx, pos)
	var ce *core.Error
	if errors.As(err, &ce) && ce.Code == "E_CURSOR_TOO_OLD" {
		agentID, wantInbox, wantBoard := wants()
		var evs []core.Event
		if wantInbox && agentID != "" {
			// ResyncFor is exclusive (mail after the cursor), and the position
			// itself may hold dropped mail, so it reads from one before.
			cursor := pos
			if cursor > 0 {
				cursor--
			}
			evs, _ = s.eng.ResyncFor(ctx, agentID, cursor)
		}
		// THE BOARD HAS CHANGED, WHATEVER IT CHANGED TO. A board-only
		// subscriber past the ring got nothing here, with its loss mark
		// already cleared, and stayed on a stale board until the next change
		// happened to reach it. One board notice says "look again"; the
		// board resource is a snapshot, so that is all a board subscriber
		// ever needs. Found by the pre-release review, round fifty-three.
		if wantBoard {
			if _, now, ierr := s.eng.SubscribeInfo(ctx, ""); ierr == nil {
				evs = append(evs, core.Event{Serial: now, Type: "board.resynced", Data: map[string]any{"resynced": true}})
			}
		}
		return evs
	}
	if err != nil || res["error"] != nil {
		return nil
	}
	evs, _ := res["events"].([]core.Event)
	return evs
}

// matchedURI returns the subscribed resource URI an event changed, or "".
func matchedURI(ev core.Event, agentID string, wantInbox, wantBoard bool) string {
	if wantInbox && ev.To == agentID && strings.HasPrefix(ev.Type, "message.") {
		return "dibs://inbox"
	}
	if wantBoard && isBoardEvent(ev) {
		return "dibs://board"
	}
	return ""
}

func honoredURIs(wantBoard, wantInbox bool) []string {
	subs := make([]string, 0, 2)
	if wantBoard {
		subs = append(subs, "dibs://board")
	}
	if wantInbox {
		subs = append(subs, "dibs://inbox")
	}
	return subs
}

func isBoardEvent(ev core.Event) bool {
	for _, pfx := range []string{"agent.", "slot.", "claim.", "board."} {
		if strings.HasPrefix(ev.Type, pfx) {
			return true
		}
	}
	return false
}

func containsStr(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

// notification builds a JSON-RPC notification, tagging it with the subscription
// id in _meta so STDIO clients can demultiplex (SEP-2575).
func notification(method string, params map[string]any, subID json.RawMessage) map[string]any {
	if params == nil {
		params = map[string]any{}
	}
	meta, _ := params["_meta"].(map[string]any)
	if meta == nil {
		meta = map[string]any{}
	}
	if len(subID) > 0 {
		meta["io.modelcontextprotocol/subscriptionId"] = subID
	}
	params["_meta"] = meta
	return map[string]any{"jsonrpc": "2.0", "method": method, "params": params}
}

// EventMetaKey and MsgTypeMetaKey name, in an inbox notification's _meta, the
// event that changed the inbox and the type of mail it carried. A subscriber
// can then apply core.WakeWorthy without a round trip. The bridge's self-wake
// had no way to tell a notify from a question and interrupted a session for
// either, which the daemon's own waker never does. Found by the pre-release
// review, round four.
const (
	EventMetaKey   = "com.dibs/event"
	MsgTypeMetaKey = "com.dibs/msg_type"
	// SerialMetaKey is the serial of the event that changed the inbox, so a
	// subscriber that reconnects can say where it left off (SinceMetaKey on
	// the listen request) and have the gap replayed from the ring. A
	// reconnect used to start at the current serial, and every message that
	// arrived in the gap produced no notification. Found by the pre-release
	// review, round twelve.
	SerialMetaKey = "com.dibs/serial"
	SinceMetaKey  = "com.dibs/since"
	// SessionMetaKey is the harness session the caller is running inside,
	// attached by the stdio bridge to every call. On a listen request it
	// names the session the stream serves: see standingFunc.
	SessionMetaKey = "com.dibs/session"
)

func resourceUpdated(uri string, subID json.RawMessage, ev core.Event) map[string]any {
	params := map[string]any{"uri": uri}
	if uri == "dibs://inbox" {
		msgType, _ := ev.Data["msg_type"].(string)
		params["_meta"] = map[string]any{EventMetaKey: ev.Type, MsgTypeMetaKey: msgType, SerialMetaKey: ev.Serial}
	}
	return notification("notifications/resources/updated", params, subID)
}

// replayGap hands a resuming subscriber the inbox events it missed, from the
// ring or, past the ring, from the inbox itself. Reports whether the stream
// is still writable.
func (s *Server) replayGap(ctx context.Context, stream sseStream, subID json.RawMessage,
	agentID string, cursor uint64, standing standingFunc,
) bool {
	missed, tooOld := s.missedFor(ctx, cursor)
	if tooOld {
		// THE RING IS NOT THE ONLY RECORD. A cursor older than the ring got
		// an empty replay after the acknowledgment, so a question that
		// arrived while the subscriber was away and whose event had since
		// left the ring sat in the inbox with no notice and no signal that
		// one was missed. The inbox says what is still owed; each waiting
		// message after the cursor is replayed as the notice the ring would
		// have carried. Found by the pre-release review, round thirty.
		missed, _ = s.eng.ResyncFor(ctx, agentID, cursor)
	}
	if s.duringReplay != nil {
		s.duringReplay()
	}
	for _, ev := range missed {
		uri := matchedURI(ev, agentID, true, false)
		if uri == "" {
			continue
		}
		// RE-READ BEFORE EACH SEND. The pre-replay check is one instant, and
		// the replay is a loop with I/O in it; an agent that moves to another
		// session, or whose token rotates, part way through catch-up must not
		// be handed the rest as inbox wakes for the session it left. The live
		// pump makes the same check per notification. Found by the pre-release
		// review, round sixty-three.
		if standing != nil {
			live, held := standing()
			if !live {
				return false
			}
			if !held {
				return true // moved away: the rest of the gap is the new session's to hear
			}
		}
		if !stream.send(resourceUpdated(uri, subID, ev)) {
			return false
		}
	}
	return true
}

// missedFor is every event the ring holds after the cursor, for the caller
// to filter. tooOld reports a cursor older than the ring, which the ring
// cannot answer and the inbox can (ResyncFor).
//
// UNCHARGED. This read the ring as the agent, and a read as the agent spends
// its rate budget: the listen that opened the stream had spent the last
// token, the replay got E_RATE_LIMITED, and an error here was an empty gap,
// so the stream proceeded from the present past a pending question. The
// replay is the daemon's own work for a subscriber it already authenticated,
// so it reads the ring the way the daemon does. Found by the pre-release
// review, round thirty-four.
func (s *Server) missedFor(ctx context.Context, cursor uint64) (evs []core.Event, tooOld bool) {
	res, err := s.eng.EventsSince(ctx, "", cursor, true)
	var ce *core.Error
	if errors.As(err, &ce) && ce.Code == "E_CURSOR_TOO_OLD" {
		return nil, true
	}
	if err != nil || res["error"] != nil {
		return nil, false
	}
	evs, _ = res["events"].([]core.Event)
	return evs, false
}

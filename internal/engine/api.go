package engine

import (
	"context"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// Do submits one mutating op to the loop and waits.
func (e *Engine) Do(ctx context.Context, op *core.Op) (core.Result, error) {
	req := request{op: op, reply: make(chan reply, 1)}
	return e.send(ctx, req)
}

// query runs fn inside the loop (read-consistent; may itself exec system ops).
func (e *Engine) query(ctx context.Context, fn func() core.Result) (core.Result, error) {
	req := request{fn: fn, reply: make(chan reply, 1)}
	return e.send(ctx, req)
}

func (e *Engine) send(ctx context.Context, req request) (core.Result, error) {
	select {
	case e.ops <- req:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case r := <-req.reply:
		return r.res, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// authRead performs the read-path phases (SPEC §2): auth, rate, ledgered wake,
// ephemeral touch, durable checkpoint coalescing. Must run inside the loop.
// Returns nil (with an error result set) if the caller is not authenticated.
func (e *Engine) authRead(token string, now time.Time) (*core.Agent, core.Result) {
	l := e.state.LaneByToken(token)
	if l == nil {
		return nil, core.Result{"error": core.ErrBadToken}
	}
	if !e.allow(l.ID, now) {
		return nil, core.Result{"error": core.ErrRateLimited}
	}
	e.wakeIfSleeping(l, now)
	e.seen[l.ID] = now
	e.touchDurable(l, now)
	return l, nil
}

// SubscribeInfo resolves what a subscriptions/listen (SEP-2575) request needs:
// the current serial to stream from, and (if an agent token is supplied) the
// authenticated agent id so an inbox subscription can be scoped to it. An empty
// token is allowed (board-only subscription); a bad token errors.
func (e *Engine) SubscribeInfo(ctx context.Context, token string) (laneID string, since uint64, err error) {
	res, qerr := e.query(ctx, func() core.Result {
		now := time.Now()
		if token == "" {
			return core.Result{"since": e.state.Serial}
		}
		l, errRes := e.authRead(token, now)
		if errRes != nil {
			return errRes
		}
		return core.Result{"lane_id": l.ID, "since": e.state.Serial}
	})
	if qerr != nil {
		return "", 0, qerr
	}
	if e2, ok := res["error"].(error); ok {
		return "", 0, e2
	}
	laneID, _ = res["lane_id"].(string)
	since, _ = res["since"].(uint64)
	return laneID, since, nil
}

// InboxFor returns the caller's decrypted mailbox for a resources/read of
// dibs://inbox (marks pending delivered, same as the inbox tool).
func (e *Engine) InboxFor(ctx context.Context, token string) (core.Result, error) {
	return e.Inbox(ctx, token)
}

// EventsSince returns buffered events after serial. Metadata only: never
// marks delivery (SPEC §8). all=true is the human observer path (no token).
func (e *Engine) EventsSince(ctx context.Context, token string, serial uint64, all bool) (core.Result, error) {
	return e.query(ctx, func() core.Result {
		now := time.Now()
		agent := ""
		if !all {
			l, errRes := e.authRead(token, now)
			if errRes != nil {
				return errRes
			}
			agent = l.ID
		}
		serial = clampCursor(serial, e.ringFloor())
		if floor := e.ringFloor(); serial+1 < floor {
			return core.Result{"error": errCursorTooOld(floor)}
		}
		return core.Result{"events": e.eventsSince(serial, agent, all), "serial": e.state.Serial}
	})
}

// AwaitEvents long-polls until an event after serial matches, or timeout.
func (e *Engine) AwaitEvents(
	ctx context.Context, token string, serial uint64, timeout time.Duration, all bool,
) (core.Result, error) {
	if timeout <= 0 || timeout > 60*time.Second {
		timeout = 60 * time.Second
	}
	ch := make(chan []core.Event, 1)
	res, err := e.query(ctx, func() core.Result {
		now := time.Now()
		agent := ""
		if !all {
			l, errRes := e.authRead(token, now)
			if errRes != nil {
				return errRes
			}
			agent = l.ID
		}
		serial = clampCursor(serial, e.ringFloor())
		if floor := e.ringFloor(); serial+1 < floor {
			return core.Result{"error": errCursorTooOld(floor)}
		}
		if evs := e.eventsSince(serial, agent, all); len(evs) > 0 {
			return core.Result{"events": evs, "serial": e.state.Serial}
		}
		e.watch = append(e.watch, waiter{
			since: serial, agent: agent, all: all, ch: ch, expires: now.Add(timeout),
		})
		return core.Result{"parked": true}
	})
	if err != nil {
		return nil, err
	}
	if _, parked := res["parked"]; !parked {
		return res, nil
	}
	select {
	case evs := <-ch:
		return core.Result{"events": evs, "serial": maxSerial(evs, serial)}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Inbox returns the caller's mailbox (non-terminal + unconsumed terminal,
// decrypted) and marks pending mail delivered via a ledgered op.
func (e *Engine) Inbox(ctx context.Context, token string) (core.Result, error) {
	return e.query(ctx, func() core.Result {
		now := time.Now()
		l, errRes := e.authRead(token, now)
		if errRes != nil {
			return errRes
		}
		e.markDelivered(l, now)
		mail := e.state.Inbox(l.ID)
		res := core.Result{
			"messages": mail, "serial": e.state.Serial,
			"truncated_before_serial": l.TruncatedBefore,
			"announcements":           e.state.UnackedFor(l.ID),
		}
		// The same mail under both names, because the two tools that return it
		// disagreed about what to call it, and each used the OTHER one's name.
		//
		// inbox returned `messages`; check_in returned `inbox`. So an agent that
		// called the inbox tool and read the inbox key got an empty list while its
		// mail sat one key away, and nothing anywhere said so. Cost a debugging
		// cycle to find from the outside, on a day spent fixing ways mail goes
		// missing; an agent would have read it as "no mail" and moved on.
		//
		// Aliased rather than renamed: `messages` is what the tool has always
		// returned and something will be reading it.
		res["inbox"] = mail
		// Surfaced here, but NOT cleared here. Exactly one call consumes a
		// notice, check_in, the documented checkpoint, because two owners of
		// a clear is how the first version of this went wrong twice over: it
		// cleared without returning anything (destroying unseen notices on an
		// ordinary read), and once that was fixed, whichever of inbox and
		// check_in an agent happened to call first silently decided which
		// response carried them.
		//
		// Read-only here is free: repeating a notice on inbox costs a line,
		// while losing one costs the agent the fact that it was admitted.
		if pending := e.pendingNotices(l.ID); len(pending) > 0 {
			res["agent_updates"] = pending
		}
		return res
	})
}

// LaneRead returns an agent's announcement history to one of its members.
//
// A read, not an op: it changes nothing, and in particular it does not acknowledge
// anything. Reading what an agent has said and accepting an obligation it placed on
// you are separate acts, and collapsing them would let a context-recovery read
// silently discharge an ack the agent never accounted for.
func (e *Engine) LaneRead(ctx context.Context, token, agent string, limit int) (core.Result, error) {
	return e.query(ctx, func() core.Result {
		l, errRes := e.authRead(token, time.Now())
		if errRes != nil {
			return errRes
		}
		ch, err := e.state.ReaderChannel(l, agent)
		if err != nil {
			// Hand the error to the loop, which turns res["error"] into a real
			// Go error: do NOT flatten it into a payload here.
			//
			// This built {code, message, hint} by hand and returned no error, so
			// the MCP layer never set isError: "not a member of this agent" and
			// "that agent does not exist" arrived as SUCCESSFUL tool calls
			// carrying a refusal in their text. A client that trusts the
			// protocol's own error signal, which is the point of having one,
			// reads that as an agent snapshot and skips the hint telling it to
			// join or subscribe.
			//
			// The payload the caller sees is unchanged: callTool unpacks a
			// core.Error into exactly those three fields. The only difference is
			// that the failure is now marked as one.
			return core.Result{"error": err}
		}
		res := core.Result{
			"lane_id": ch.ID, "topic": ch.Topic, "members": len(ch.Members),
			"announcements": e.state.LaneHistory(ch, l.ID, limit),
			// Posts are here because this is the only place they can be read.
			// The agent.post event carries metadata only (SPEC §10), so without
			// this a remark was write-only: delivered once to whoever happened
			// to be polling, and unreachable to everyone else forever.
			"posts": e.state.PostHistory(ch, limit),
		}
		// The coordination key goes to MEMBERS only. An agent that lost its
		// context lost the key with it, and a key it cannot recover is a key it
		// stops declaring, which quietly returns the fleet to guessing. But a
		// subscriber is not a member: it watches the agent without holding the
		// key, and handing it one would let it claim a membership it does not
		// have.
		if _, member := ch.Members[l.ID]; member || e.state.SpeaksFor(ch, l.ID) != "" {
			res["key"] = ch.Key
		} else {
			res["subscribed"] = true
		}
		return res
	})
}

// GetMessage returns one full message for its sender or recipient (SPEC §8).
func (e *Engine) GetMessage(ctx context.Context, token string, serial uint64) (core.Result, error) {
	return e.query(ctx, func() core.Result {
		now := time.Now()
		l, errRes := e.authRead(token, now)
		if errRes != nil {
			return errRes
		}
		m, ok := e.state.Messages[serial]
		if !ok || (m.From != l.ID && m.To != l.ID) {
			// An ANNOUNCEMENT serial is the overwhelmingly likely mistake here,
			// because the wake nudge hands the agent a serial and says to go
			// read it, and a serial in hand makes read_mail the obvious call.
			//
			// Answering "no message N addressed to you" for a thing that plainly
			// exists is the failure this codebase keeps producing: a confident,
			// specific and false statement, from which the only reasonable
			// conclusion is that the announcement was withdrawn. A reviewing
			// agent reached exactly that conclusion and messaged a human.
			if an, isAnnounce := e.state.Announcements[serial]; isAnnounce {
				return core.Result{"error": core.ErrWrongKind(serial, an.Space)}
			}
			return core.Result{"error": core.ErrNoMessage(serial, l.TruncatedBefore)}
		}
		if m.To == l.ID && m.State == core.MsgStatePending {
			_, _ = e.applyAndLedger(&core.Op{Kind: core.OpMarkDelivered, MsgSerials: []uint64{serial}}, now)
		}
		return core.Result{"message": m, "serial": e.state.Serial}
	})
}

// markDelivered ledgers pending→delivered for the agent's mailbox.
func (e *Engine) markDelivered(l *core.Agent, now time.Time) {
	var serials []uint64
	for _, m := range e.state.Messages {
		if m.To == l.ID && m.State == core.MsgStatePending {
			serials = append(serials, m.Serial)
		}
	}
	if len(serials) > 0 {
		_, _ = e.applyAndLedger(&core.Op{Kind: core.OpMarkDelivered, MsgSerials: serials}, now)
	}
}

// Board returns the public snapshot with presentation annotations (SPEC §2):
// last_seen (ephemeral freshness) and proc_alive, computed at read time.
func (e *Engine) Board(ctx context.Context) (core.Result, error) {
	return e.query(ctx, func() core.Result { return e.decoratedBoard() })
}

func (e *Engine) decoratedBoard() core.Result {
	b := e.state.Board()
	agents, _ := b["agents"].([]map[string]any)
	for _, lm := range agents {
		id, _ := lm["id"].(string)
		l := e.state.Agents[id]
		if l == nil {
			continue
		}
		seen := l.LastCoordination
		if t, ok := e.seen[id]; ok && t.After(seen) {
			seen = t
		}
		lm["last_seen"] = seen
		lm["status"] = l.Status
		if l.PID != 0 && e.prober != nil {
			lm["proc_alive"] = e.prober.Alive(l.PID)
		}
	}
	return core.Result(b)
}

// AllMessages is the human admin surface (decrypted).
func (e *Engine) AllMessages(ctx context.Context) (core.Result, error) {
	return e.query(ctx, func() core.Result {
		out := make([]*core.Message, 0, len(e.state.Messages))
		for _, m := range e.state.Messages {
			out = append(out, m)
		}
		return core.Result{"messages": out}
	})
}

// Subscribe attaches an SSE stream fed from serial onward.
func (e *Engine) Subscribe(since uint64) (<-chan core.Event, func()) {
	ch := make(chan core.Event, 256)
	e.subs <- subReq{ch: ch, since: since}
	return ch, func() { e.unsubs <- ch }
}

func maxSerial(evs []core.Event, fallback uint64) uint64 {
	m := fallback
	for _, ev := range evs {
		if ev.Serial > m {
			m = ev.Serial
		}
	}
	return m
}

// clampCursor treats 0 as "from wherever the ring starts".
//
// 0 is the only cursor an agent can pick without having seen the board first,
// it is the obvious "give me everything" value, and every agent reaches for it.
// Erroring on it means the first call an agent makes is the one that fails, and
// the error is about ring-buffer internals it has no way to know. A real lost
// cursor (a stale non-zero serial) still errors, because there the agent DID
// have a position and genuinely missed events; that distinction is worth
// keeping.
func clampCursor(serial, floor uint64) uint64 {
	if serial == 0 && floor > 0 {
		return floor - 1
	}
	return serial
}

func errCursorTooOld(floor uint64) error {
	return core.ErrCursorTooOld(floor)
}

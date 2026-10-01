package engine

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// An approved request is work the agent said it would do.
//
// Until the agent reports it done (respond disposition "done"), it is an open
// obligation and counts exactly like a declaration that says the agent is
// working: a turn Dibs started that ends with one open is continued
// (continuation.go), an agent that keeps stopping is woken again and then
// reported stalled (stall.go), and the row lists what it owes. Part E of the
// design agreed with k7-dev (Dibs #1129): a worker that approved #1123 and
// stopped looked exactly like one that had finished it.
//
// Bounded to core.ObligationWindow after approval. Approval has always existed and
// "done" has not, so every request approved before this shipped would
// otherwise read as owed forever, and an agent with a long history would be
// woken for work it finished weeks ago. A day covers a worker's real
// backlog; an older approval is history.

const maxObligationQuote = 200

// obligationsOf are the requests this agent approved and has not reported
// done, newest first. On the writer loop.
func (e *Engine) obligationsOf(agent string, now time.Time) []*core.Message {
	var owed []*core.Message
	for _, m := range e.state.Messages {
		if m.To != agent || !m.Owed(now) {
			continue
		}
		owed = append(owed, m)
	}
	sort.Slice(owed, func(i, j int) bool { return owed[i].Serial > owed[j].Serial })
	return owed
}

// workSlotsOf is everything the agent is committed to: its declarations, and
// each open obligation as a declaration in its own words would read. The
// obligation's version is its serial, so a new approval is progress and
// reporting one done removes it.
func (e *Engine) workSlotsOf(l *core.Agent, now time.Time) []core.Slot {
	slots := slotsOf(l)
	for _, m := range e.obligationsOf(l.ID, now) {
		body := strings.Join(strings.Fields(m.Body), " ")
		if len(body) > maxObligationQuote {
			body = body[:maxObligationQuote] + "..."
		}
		owed := core.Slot{
			ID:            fmt.Sprintf("request %d", m.Serial),
			Text:          fmt.Sprintf("approved request from %s, not yet reported done: %s", m.From, body),
			UpdatedSerial: m.Serial,
		}
		if park, ok := parkedBy(slots, m.Serial); ok {
			owed.Waiting, owed.RecheckSec = park.Waiting, park.RecheckSec
			owed.UpdatedSerial = max(owed.UpdatedSerial, park.UpdatedSerial)
		}
		slots = append(slots, owed)
	}
	return slots
}

// parkedBy is the waiting declaration that names an owed request, if any.
//
// An owed request blocked on somebody else could not be parked: it was added
// as work in progress whatever the agent declared, so a worker whose published
// work was held by an owner decision was continued for it again and again,
// and its only way out was a "done" that would have been false. Reported by
// k7-dev for request #2540. A declaration with `waiting` set and
// `request:<serial>` (or `msg:<serial>`) in its refs now parks that request:
// it inherits the wait and the recheck, and is left alone until either is
// due. Linked explicitly, by the agent, because a wait on one thing is not a
// wait on everything it owes.
func parkedBy(slots []core.Slot, serial uint64) (core.Slot, bool) {
	want := map[string]bool{
		fmt.Sprintf("request:%d", serial): true,
		fmt.Sprintf("msg:%d", serial):     true,
	}
	for _, s := range slots {
		if strings.TrimSpace(s.Waiting) == "" {
			continue
		}
		for _, r := range s.Refs {
			if want[strings.ToLower(strings.TrimSpace(r))] {
				return s, true
			}
		}
	}
	return core.Slot{}, false
}

// owedSerials is the row's `owes`.
func (e *Engine) owedSerials(agent string, now time.Time) []uint64 {
	var out []uint64
	for _, m := range e.obligationsOf(agent, now) {
		out = append(out, m.Serial)
	}
	return out
}

// UnansweredFrom is the note for a send that looks like an answer and is not
// one: the caller still owes `to` a response to a question or request `to`
// sent it, and a send does not give one.
//
// Measured 2026-10-01: a worker accepted request #2500 with a notify quoting
// "request2500", the request expired unanswered ten minutes later, and the
// task looked dropped although the worker had taken it. A notify is not a
// verdict, so nothing in the fold can treat it as one; what Dibs can do is
// say so at the moment it is sent.
func (e *Engine) UnansweredFrom(ctx context.Context, token, to string) string {
	res, err := e.query(ctx, func() core.Result {
		l, errRes := e.authRead(token, time.Now())
		if errRes != nil || l == nil {
			return core.Result{}
		}
		return core.Result{"note": unansweredNote(e.state, l.ID, to, time.Now())}
	})
	if err != nil {
		return ""
	}
	n, _ := res["note"].(string)
	return n
}

// unansweredNote is the decision, apart from the engine.
func unansweredNote(st *core.State, me, to string, now time.Time) string {
	var owed []*core.Message
	for _, m := range st.Messages {
		if m.To == me && m.From == to && m.Expecting() && !m.Terminal() {
			owed = append(owed, m)
		}
	}
	if len(owed) == 0 {
		return ""
	}
	sort.Slice(owed, func(i, j int) bool { return owed[i].Serial < owed[j].Serial })
	var parts []string
	for _, m := range owed {
		verbs := "answer|decline"
		if m.Type == core.MsgRequest {
			verbs = "approve|deny|decline"
		}
		left := ""
		if !m.Deadline.IsZero() {
			left = fmt.Sprintf(", %s left", m.Deadline.Sub(now).Round(time.Minute))
		}
		parts = append(parts, fmt.Sprintf("#%d (a %s%s): respond(%d, %s)", m.Serial, m.Type, left, m.Serial, verbs))
	}
	return "sent, but this does not answer what " + to + " is waiting on from you: " +
		strings.Join(parts, "; ") + ". A message is not a response, so the ask stays open and " +
		"expires unanswered at its deadline"
}

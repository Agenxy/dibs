package engine

import (
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
		slots = append(slots, core.Slot{
			ID:            fmt.Sprintf("request %d", m.Serial),
			Text:          fmt.Sprintf("approved request from %s, not yet reported done: %s", m.From, body),
			UpdatedSerial: m.Serial,
		})
	}
	return slots
}

// owedSerials is the row's `owes`.
func (e *Engine) owedSerials(agent string, now time.Time) []uint64 {
	var out []uint64
	for _, m := range e.obligationsOf(agent, now) {
		out = append(out, m.Serial)
	}
	return out
}

package engine

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/wakeexec"
)

// When continuing at Stop is not enough: waking again later, and saying so
// when that does not work either.
//
// continuation.go continues a turn in place, twice. That covers an agent that
// stops by mistake. It does not cover one that stops twice in quick succession
// and then sits, or one whose declared wait has a recheck time that no message
// will mark. For those the board wakes the agent again on a backoff (10, 30 and
// 60 minutes after its last turn ended), through the same route as mail, with
// a fixed sentence that names no content. When those run out with nothing
// changed, the agent is STALLED: its row says so, and whoever assigned the
// work is told once. That is part D of the design agreed with k7-dev (Dibs
// #1129), and the reason is the operator's: manual intervention is a bug, so
// an orchestrator must not have to notice a stall by reading logs.
//
// The same guard as continuation.go: only an agent whose current turn chain a
// Dibs wake started, and never after a person prompted. A recheck is the
// exception, because the agent asked for it by declaring one.
//
// Ephemeral, like every other liveness clock here: a restart forgets the
// count and begins again, which costs at most one extra round of wakes.

var continuationBackoff = []time.Duration{10 * time.Minute, 30 * time.Minute, 60 * time.Minute}

const maxRechecks = 3

// stallEvery paces stallTick; a variable so a test can run the real loop
// without waiting minutes for it.
var stallEvery = 15 * time.Second

// workRecord is one agent's progress on the declarations it holds now.
type workRecord struct {
	version   uint64 // newest UpdatedSerial among its declarations
	versionAt time.Time
	wakes     int // continuation wakes sent for this version
	rechecks  int
	stalledAt time.Time
	told      bool
}

// workAction is what the board does about one agent on this tick.
type workAction int

const (
	workNothing workAction = iota
	workContinue
	workRecheck
	workStall
)

// workInput is everything decideWork reads, so it is testable without an
// engine.
type workInput struct {
	slots   []core.Slot
	started bool      // a Dibs wake started this agent's current turn chain
	ended   time.Time // when its last turn ended; zero while one is running
	idle    bool      // no turn running: ended, or its process is gone
}

// decideWork is the decision for one agent on one tick.
func decideWork(in workInput, rec workRecord, now time.Time) (workAction, workRecord) {
	if len(in.slots) == 0 {
		return workNothing, workRecord{}
	}
	version, open, recheck := summarizeSlots(in.slots)
	if rec.version != version || rec.versionAt.IsZero() {
		rec = workRecord{version: version, versionAt: now} // progress: the declaration moved
	}
	if !rec.stalledAt.IsZero() {
		return workNothing, rec // stays stalled until the declaration changes
	}
	if open > 0 {
		return decideOpenWork(in, rec, now)
	}
	return decideRecheck(in, rec, recheck, now)
}

// summarizeSlots is the declarations' version, how many say working, and the
// shortest recheck among the waiting ones.
func summarizeSlots(slots []core.Slot) (version uint64, open, recheck int) {
	for _, s := range slots {
		version = max(version, s.UpdatedSerial)
		switch {
		case strings.TrimSpace(s.Waiting) == "":
			open++
		case s.RecheckSec > 0 && (recheck == 0 || s.RecheckSec < recheck):
			recheck = s.RecheckSec
		}
	}
	return version, open, recheck
}

// decideOpenWork: work in progress, and a turn that ended on it.
func decideOpenWork(in workInput, rec workRecord, now time.Time) (workAction, workRecord) {
	if !in.started || in.ended.IsZero() {
		return workNothing, rec // a person's session, or a turn still running
	}
	if rec.wakes >= len(continuationBackoff) {
		rec.stalledAt = now
		return workStall, rec
	}
	if now.Before(in.ended.Add(continuationBackoff[rec.wakes])) {
		return workNothing, rec
	}
	rec.wakes++
	return workContinue, rec
}

// decideRecheck: everything is waiting, and a recheck the agent asked for.
func decideRecheck(in workInput, rec workRecord, recheck int, now time.Time) (workAction, workRecord) {
	if recheck == 0 || !in.idle {
		return workNothing, rec // waiting on something that will say so itself
	}
	if now.Before(rec.versionAt.Add(time.Duration(recheck*(rec.rechecks+1)) * time.Second)) {
		return workNothing, rec
	}
	if rec.rechecks >= maxRechecks {
		rec.stalledAt = now
		return workStall, rec
	}
	rec.rechecks++
	return workRecheck, rec
}

// workStateOf is the row's `work`: idle, working, waiting or stalled. Derived
// from what the agent declared and what the board has seen it do, never from
// whether its process is alive: a Codex agent in the ChatGPT app has no
// process between calls, and its row said "dormant (process gone)" while it
// was busy. Reported by k7-dev (Dibs #1152).
func (e *Engine) workStateOf(l *core.Agent) string {
	slots := e.workSlotsOf(l, time.Now())
	if len(slots) == 0 {
		return "idle"
	}
	e.wakers.mu.Lock()
	rec, ok := e.wakers.work[l.ID]
	e.wakers.mu.Unlock()
	if ok && !rec.stalledAt.IsZero() {
		return "stalled"
	}
	if len(openOf(slots)) > 0 {
		return "working"
	}
	return "waiting"
}

// stallTick runs the decision for every agent. On the writer loop, from Run.
func (e *Engine) stallTick(now time.Time) {
	if e.state == nil || now.Sub(e.lastStallTick) < stallEvery {
		return
	}
	e.lastStallTick = now
	for _, id := range sortedAgentIDs(e.state) {
		l := e.state.Agents[id]
		if l.Retired() || e.isTheHuman(l.ID) {
			continue
		}
		e.wakers.mu.Lock()
		_, started := e.wakers.dibsTurn[l.ID]
		rec := e.wakers.work[l.ID]
		e.wakers.mu.Unlock()
		ended := e.turnEnded[l.ID]
		in := workInput{
			slots: e.workSlotsOf(l, now), started: started, ended: ended,
			idle: !ended.IsZero() || l.Status != core.StatusActive,
		}
		action, next := decideWork(in, rec, now)
		e.wakers.mu.Lock()
		if e.wakers.work == nil {
			e.wakers.work = map[string]workRecord{}
		}
		if len(in.slots) == 0 {
			delete(e.wakers.work, l.ID)
		} else {
			e.wakers.work[l.ID] = next
		}
		e.wakers.mu.Unlock()
		switch action {
		case workNothing:
		case workContinue:
			e.wakeForWork(l, wakeexec.KindContinuation)
		case workRecheck:
			e.wakeForWork(l, wakeexec.KindRecheck)
		case workStall:
			e.reportWorkStall(l, next, now)
		}
	}
}

// wakeForWork wakes an agent for its own work through the route mail takes.
func (e *Engine) wakeForWork(l *core.Agent, kind string) {
	plan, ok := e.wakeFor(l, kind, core.Event{Type: "work." + kind, To: l.ID})
	if !ok {
		return
	}
	agent, thread := l.ID, plan.thread
	slog.Info("waking an agent for work it declared and has not finished", "agent", agent, "kind", kind)
	go func() {
		defer e.wakeExited(agent, thread)
		e.runWakeAndReport(plan, agent)
	}()
}

// reportWorkStall tells whoever assigned the work, once per declaration version.
func (e *Engine) reportWorkStall(l *core.Agent, rec workRecord, now time.Time) {
	assigner := e.assignerOf(l.ID)
	body := stallBody(l, e.workSlotsOf(l, now), rec, now)
	slog.Warn("an agent has stalled on work it declared", "agent", l.ID, "tell", assigner)
	go e.sendStallNotice(l.ID, assigner, body)
}

// assignerOf is who gave this agent its work: the sender of the newest request
// it approved. "" when there is none, and the notice goes to a coordinator or
// the human instead.
func (e *Engine) assignerOf(agent string) string {
	var best *core.Message
	for _, m := range e.state.Messages {
		if m.To != agent || m.Type != core.MsgRequest || m.State != core.MsgStateApproved || m.From == agent {
			continue
		}
		if best == nil || m.Serial > best.Serial {
			best = m
		}
	}
	if best == nil {
		return ""
	}
	if from := e.state.Agents[best.From]; from == nil || from.Retired() {
		return ""
	}
	return best.From
}

func (e *Engine) sendStallNotice(agent, to, body string) {
	if to == "" {
		to = e.coordinatorOrHuman()
	}
	if to == "" || to == agent {
		slog.Warn("an agent stalled and there is nobody to tell", "agent", agent)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	from, token, err := e.dibsAgent(ctx)
	if err != nil || from == to {
		return
	}
	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpSendMessage, Token: token, To: to, MsgType: core.MsgNotify, Body: body,
	}); err != nil {
		slog.Warn("could not report a stalled agent", "agent", agent, "err", err)
	}
}

// stallBody is the notice, apart from sending it so the wording is testable.
func stallBody(l *core.Agent, slots []core.Slot, rec workRecord, now time.Time) string {
	var texts []string
	for _, s := range slots {
		texts = append(texts, fmt.Sprintf("%q", s.Text))
	}
	tries := fmt.Sprintf("%d continuation wake(s)", rec.wakes)
	if rec.rechecks > 0 {
		tries = fmt.Sprintf("%d recheck wake(s)", rec.rechecks)
	}
	return fmt.Sprintf("%s has stalled. It declares %s and has not changed that through %s "+
		"over %s, and no turn is running. Its board row says `stalled`. It stays that way "+
		"until its declaration changes; the board will not wake it for this again.",
		l.ID, strings.Join(texts, ", "), tries, now.Sub(rec.versionAt).Round(time.Minute))
}

// slotsOf is the agent's declarations in slot-id order, so a notice reads the
// same every time.
func slotsOf(l *core.Agent) []core.Slot {
	out := make([]core.Slot, 0, len(l.Slots))
	for _, s := range l.Slots {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// sortedAgentIDs, so a tick visits agents in the same order every time.
func sortedAgentIDs(s *core.State) []string {
	ids := make([]string, 0, len(s.Agents))
	for id := range s.Agents {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// workNotice is a continuation or recheck wake as the socket carries it: the
// declarations in the agent's own words, which argv may not carry.
func (e *Engine) workNotice(l *core.Agent, kind string) string {
	var b strings.Builder
	if kind == wakeexec.KindRecheck {
		b.WriteString("Dibs: a wait you declared is due for a recheck:")
	} else {
		b.WriteString("Dibs: work you declared is still open and no turn is running:")
	}
	for _, s := range e.workSlotsOf(l, time.Now()) {
		text := s.Text
		if len(text) > maxQuoted {
			text = text[:maxQuoted] + "..."
		}
		fmt.Fprintf(&b, "\n  %s: %q", s.ID, text)
		if s.Waiting != "" {
			fmt.Fprintf(&b, " (waiting on %s)", s.Waiting)
		}
	}
	b.WriteString("\nIf it is finished, undeclare it; if it is blocked, declare it again with `waiting`.")
	return b.String()
}

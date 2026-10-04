package engine

import (
	"fmt"
	"strings"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/wakeexec"
)

type socketWait struct {
	version uint64
	next    time.Time
	writes  int
}

type socketWorkKey struct {
	key     string
	version uint64
}

type socketBackoff struct {
	kind    string
	version uint64
}

// The same derived start-of-observation clock as workRecord.versionAt, per
// slot. A short wait cannot make a longer one due, or reset its clock. Hook
// delivery advances the cadence without spending a socket retry.
func (e *Engine) observeSocketWaits(l *core.Agent, now time.Time) {
	if e.socketWaits == nil {
		e.socketWaits = map[string]socketWait{}
	}
	live := map[string]bool{}
	for _, s := range e.workSlotsOf(l, now) {
		if s.Waiting == "" || s.RecheckSec == 0 {
			continue
		}
		key := l.ID + "\x00" + s.ID
		live[key] = true
		if rec, ok := e.socketWaits[key]; !ok || rec.version != s.UpdatedSerial {
			e.socketWaits[key] = socketWait{
				version: s.UpdatedSerial, next: now.Add(time.Duration(s.RecheckSec) * time.Second),
			}
		}
	}
	for key := range e.socketWaits {
		if strings.HasPrefix(key, l.ID+"\x00") && !live[key] {
			delete(e.socketWaits, key)
		}
	}
}

func (e *Engine) dueSocketWaits(l *core.Agent, now time.Time) ([]core.Slot, []socketWorkKey) {
	e.observeSocketWaits(l, now)
	var slots []core.Slot
	var keys []socketWorkKey
	for _, s := range e.workSlotsOf(l, now) {
		key := l.ID + "\x00" + s.ID
		rec, ok := e.socketWaits[key]
		if ok && rec.version == s.UpdatedSerial && rec.writes < maxRechecks && !now.Before(rec.next) {
			slots = append(slots, s)
			keys = append(keys, socketWorkKey{key, rec.version})
		}
	}
	return slots, keys
}

func (e *Engine) socketWorkDigest(l *core.Agent, now time.Time) string {
	slots, _ := e.dueSocketWaits(l, now)
	text := ""
	if len(slots) > 0 {
		text = workSlotsNotice(slots, wakeexec.KindRecheck)
	}
	if backoff, ok := e.socketBackoff[l.ID]; ok {
		version, _, _ := summarizeSlots(e.workSlotsOf(l, now))
		if version != backoff.version {
			delete(e.socketBackoff, l.ID)
		} else {
			if text != "" {
				text += "\n"
			}
			text += e.workNotice(l, backoff.kind)
		}
	}
	return text
}

func (e *Engine) markSocketWork(
	l *core.Agent, keys []socketWorkKey, backoff socketBackoff, now time.Time, written bool,
) {
	if written && len(keys) > 0 {
		e.noteSocketRecheck(l, now)
	}
	for _, k := range keys {
		rec, ok := e.socketWaits[k.key]
		if !ok || rec.version != k.version {
			continue // a later declaration was not quoted in this presentation
		}
		for _, s := range e.workSlotsOf(l, now) {
			if l.ID+"\x00"+s.ID == k.key && s.UpdatedSerial == k.version {
				rec.next = now.Add(time.Duration(s.RecheckSec) * time.Second)
				if written {
					rec.writes++
				}
				e.socketWaits[k.key] = rec
			}
		}
	}
	if current, ok := e.socketBackoff[l.ID]; ok && current == backoff {
		delete(e.socketBackoff, l.ID)
	}
}

func socketWorkRecord(slots []core.Slot, rec workRecord, now time.Time) workRecord {
	version, _, _ := summarizeSlots(slots)
	if rec.version != version || rec.versionAt.IsZero() {
		return workRecord{version: version, versionAt: now}
	}
	return rec
}

func (e *Engine) noteSocketRecheck(l *core.Agent, now time.Time) {
	e.wakers.mu.Lock()
	defer e.wakers.mu.Unlock()
	if e.wakers.work == nil {
		e.wakers.work = map[string]workRecord{}
	}
	rec := socketWorkRecord(e.workSlotsOf(l, now), e.wakers.work[l.ID], now)
	rec.rechecks++
	e.wakers.work[l.ID] = rec
}

// Preserve the old stalled-row/assigner report after the bounded native
// rechecks. A later wait must finish its own cadence before the whole agent
// is called stalled. This is a tick decision, never a digest-read side effect.
func (e *Engine) socketWaitStall(l *core.Agent, rec workRecord, now time.Time) {
	slots := e.workSlotsOf(l, now)
	rec = socketWorkRecord(slots, rec, now)
	if rec.stalledAt.IsZero() && e.socketLifecycle(l, now) != "busy" && e.socketWaitsExhausted(l, slots, now) {
		rec.stalledAt = now
		e.reportWorkStall(l, rec, now)
	}
	e.wakers.mu.Lock()
	if e.wakers.work == nil {
		e.wakers.work = map[string]workRecord{}
	}
	if len(slots) == 0 {
		delete(e.wakers.work, l.ID)
	} else {
		e.wakers.work[l.ID] = rec
	}
	e.wakers.mu.Unlock()
}

func (e *Engine) socketWaitsExhausted(l *core.Agent, slots []core.Slot, now time.Time) bool {
	found := false
	for _, s := range slots {
		if s.RecheckSec == 0 || s.Waiting == "" {
			continue
		}
		found = true
		rec := e.socketWaits[l.ID+"\x00"+s.ID]
		if rec.version != s.UpdatedSerial || rec.writes < maxRechecks || now.Before(rec.next) {
			return false
		}
	}
	return found
}

func (e *Engine) socketWorkInput(l *core.Agent, in workInput, now time.Time) (workInput, bool) {
	if !harnessSpeaksSocket(l) {
		return in, false
	}
	if e.socketLifecycle(l, now) == "busy" {
		in.idle, in.ended = false, time.Time{}
	}
	_, open, _ := summarizeSlots(in.slots)
	return in, open == 0
}

func workSlotsNotice(slots []core.Slot, kind string) string {
	var b strings.Builder
	if kind == wakeexec.KindRecheck {
		b.WriteString("Dibs: a wait you declared is due for a recheck:")
	} else {
		b.WriteString("Dibs: work you declared is still open and no turn is running:")
	}
	for _, s := range slots {
		text := s.Text
		if len(text) > maxQuoted {
			text = text[:maxQuoted] + "..."
		}
		fmt.Fprintf(&b, "\n  %s: %q", s.ID, text)
		if s.Waiting != "" {
			fmt.Fprintf(&b, " (waiting on %s)", s.Waiting)
		}
	}
	b.WriteString("\nIf it is finished, undeclare it; if it is blocked, call declare with the same " +
		"slot_id and the `waiting` argument set, which writing \"waiting\" in the text does not do.")
	return b.String()
}

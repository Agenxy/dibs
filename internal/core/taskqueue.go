package core

import (
	"sort"
	"time"
)

const (
	MsgStateQueued     = "queued"
	OpQueueUpdate      = "queue_update"
	PermQueueOrderLock = "queue_order_lock"
)

func priorityValue(p string) int {
	switch p {
	case "low":
		return 0
	case "", "normal":
		return 1
	case "high":
		return 2
	case "urgent":
		return 3
	}
	return -1
}

func checkQueue(op *Op, lim Limits) error {
	if op.Kind == OpRespond && op.Disposition == "queue" && len(op.Body) > lim.MaxBodyBytes {
		return errTooLarge("response body", lim.MaxBodyBytes)
	}
	if op.RequestPriority != "" && (op.Kind != OpSendMessage || op.MsgType != MsgRequest || op.Grant != "" || op.Adopt != "" || priorityValue(op.RequestPriority) < 0) {
		return errf("E_BAD_ARG", "priority is low|normal|high|urgent on an ordinary request", "invalid request priority")
	}
	if op.Kind != OpQueueUpdate {
		if op.QueuePriority != "" || op.QueueResetPriority || op.QueueBefore != 0 || op.QueueTail {
			return errf("E_BAD_ARG", "use queue_update for ordering changes", "queue fields on another operation")
		}
		return nil
	}
	if op.MsgSerial == 0 || (op.QueuePriority == "" && !op.QueueResetPriority && op.QueueBefore == 0 && !op.QueueTail) {
		return errf("E_BAD_ARG", "name a queued msg_serial and priority, reset_priority, before or tail", "empty queue update")
	}
	if priorityValue(op.QueuePriority) < 0 || (op.QueuePriority != "" && op.QueueResetPriority) || (op.QueueBefore != 0 && op.QueueTail) || op.QueueBefore == op.MsgSerial {
		return errf("E_BAD_ARG", "use a valid priority and one ordering action with a different anchor", "conflicting queue update")
	}
	return nil
}

// EffectivePriority keeps the sender's request intact when its recipient overrides it.
func (m *Message) EffectivePriority() string {
	p := m.QueuePriority
	if p == "" {
		p = m.RequestPriority
	}
	if p == "" {
		p = "normal"
	}
	return p
}

// TaskQueue is derived from message ownership. Adoption/merge cannot leave a stale
// second index pointing into someone else's mailbox; ranks travel with the mail.
func (s *State) TaskQueue(agent string) []*Message {
	var q []*Message
	for _, m := range s.Messages {
		if m.To == agent && m.State == MsgStateQueued {
			q = append(q, m)
		}
	}
	sort.Slice(q, func(i, j int) bool {
		if q[i].QueueRank != q[j].QueueRank {
			return q[i].QueueRank < q[j].QueueRank
		}
		return q[i].Serial < q[j].Serial
	})
	return q
}

func queueLess(a, b *Message) bool {
	if x, y := priorityValue(a.EffectivePriority()), priorityValue(b.EffectivePriority()); x != y {
		return x > y
	}
	if a.Deadline.IsZero() != b.Deadline.IsZero() {
		return !a.Deadline.IsZero()
	}
	if !a.Deadline.Equal(b.Deadline) {
		return a.Deadline.Before(b.Deadline)
	}
	return a.Serial < b.Serial
}

func insertQueued(q []*Message, m *Message) []*Message {
	i := len(q)
	for n, x := range q {
		if queueLess(m, x) {
			i = n
			break
		}
	}
	q = append(q, nil)
	copy(q[i+1:], q[i:])
	q[i] = m
	return q
}

func (s *State) setQueueOrder(agent string, q []*Message, by string, now time.Time) {
	for i, m := range q {
		if m.QueueRank != i+1 {
			m.QueueRank = i + 1
			m.QueueBy = by
			m.QueueChangedAt = now
			m.QueueChangedSerial = s.Serial + 1
		}
	}
}

// QueuePosition is a live ordinal, never a promise that a later arrival cannot shift it.
func (s *State) QueuePosition(m *Message) int {
	for i, x := range s.TaskQueue(m.To) {
		if x.Serial == m.Serial {
			return i + 1
		}
	}
	return 0
}

// AcceptedDebtCount counts durable accepted work against the existing mailbox bound.
func (s *State) AcceptedDebtCount(agent string) int {
	n := 0
	for _, m := range s.Messages {
		if m.To == agent && m.QueueDebt && (m.State == MsgStateApproved || m.State == MsgStateQueued) {
			n++
		}
	}
	return n
}

func (s *State) applyQueue(l *Agent, m *Message, op *Op, now time.Time) (Result, []Event, error) {
	if m.State == MsgStateQueued {
		return Result{"ok": true, "state": MsgStateQueued, "queue_position": s.QueuePosition(m), "changed": false}, nil, nil
	}
	if m.Terminal() {
		return nil, nil, errf("E_MSG_FINAL", "queue an unanswered ordinary request; park started work with declare(waiting)", "message already %s", m.State)
	}
	if m.Type != MsgRequest || m.Grant != "" || m.Adopt != "" || m.From == m.To {
		return nil, nil, errf("E_BAD_DISPOSITION", "queue accepts ordinary work requests only; use approve for a grant or adoption", "not queueable work")
	}
	if s.AcceptedDebtCount(l.ID) >= s.Limits.MaxMailboxDepth {
		return nil, nil, errf("E_MAILBOX_FULL", "complete or decline owed work before accepting more", "accepted work capacity reached")
	}
	if err := declareMilestones(m, op, MsgStateQueued); err != nil {
		return nil, nil, err
	}
	q := insertQueued(s.TaskQueue(l.ID), m)
	m.State = MsgStateQueued
	m.QueueDebt = op.QueueDebt
	m.Response = op.Body
	m.Consumed = true
	m.TerminalAt = now
	m.RespondedAt = s.Serial + 1
	m.OutcomeReadAt = 0
	s.setQueueOrder(l.ID, q, l.ID, now)
	evs := s.queueEvents(q, l.ID, "message.queued", m.Serial)
	s.finish(&evs, now)
	return Result{"ok": true, "state": MsgStateQueued, "queue_position": s.QueuePosition(m)}, evs, nil
}

func (s *State) queueEvents(q []*Message, by, kind string, serial uint64) []Event {
	var evs []Event
	for _, m := range q {
		if m.QueueChangedSerial == s.Serial+1 || m.Serial == serial {
			evs = append(evs, Event{Type: kind, Agent: by, To: m.From, Data: map[string]any{"msg_serial": m.Serial, "queue_position": s.QueuePosition(m), "priority": m.EffectivePriority(), "by": by}})
		}
	}
	return evs
}

func (s *State) applyQueueUpdate(l *Agent, op *Op, now time.Time) (Result, []Event, error) {
	m := s.Messages[op.MsgSerial]
	if m == nil || m.To != l.ID {
		return nil, nil, errf("E_NO_MESSAGE", "check_in lists your own queue; use its request serial", "not your queued request")
	}
	if m.State != MsgStateQueued {
		return nil, nil, errf("E_BAD_DISPOSITION", "queue_update changes queued work only", "request is %s", m.State)
	}
	if l.HasPermission(PermQueueOrderLock) || m.QueueOrderLocked {
		return nil, nil, errf("E_QUEUE_LOCKED", "ask the human or coordinator to revoke the queue_order_lock permission; starting remains allowed", "queue ordering is locked")
	}
	old := s.TaskQueue(l.ID)
	q := make([]*Message, 0, len(old))
	for _, x := range old {
		if x != m {
			q = append(q, x)
		}
	}
	nextPriority := m.QueuePriority
	if op.QueuePriority != "" {
		nextPriority = op.QueuePriority
	}
	if op.QueueResetPriority {
		nextPriority = ""
	}
	// Compute on a copy until every lock check has succeeded.
	cp := *m
	cp.QueuePriority = nextPriority
	if op.QueueBefore != 0 {
		at := -1
		for i, x := range q {
			if x.Serial == op.QueueBefore {
				at = i
				break
			}
		}
		if at < 0 {
			return nil, nil, errf("E_BAD_ARG", "choose before from check_in's current queue", "anchor is not a queued sibling")
		}
		q = append(q, nil)
		copy(q[at+1:], q[at:])
		q[at] = &cp
	} else if op.QueueTail {
		q = append(q, &cp)
	} else if op.QueuePriority != "" || op.QueueResetPriority {
		q = insertQueued(q, &cp)
	} else {
		return nil, nil, errf("E_BAD_ARG", "provide an ordering change", "no change requested")
	}
	oldIndex := map[uint64]int{}
	newIndex := map[uint64]int{}
	for i, x := range old {
		oldIndex[x.Serial] = i
	}
	for i, x := range q {
		newIndex[x.Serial] = i
	}
	for _, x := range old {
		if x.QueueOrderLocked && (oldIndex[m.Serial] < oldIndex[x.Serial]) != (newIndex[m.Serial] < newIndex[x.Serial]) {
			return nil, nil, errf("E_QUEUE_LOCKED", "ask the human or coordinator to unlock the task before crossing it", "move crosses locked request %d", x.Serial)
		}
	}
	changed := m.QueuePriority != nextPriority
	for i, x := range old {
		if q[i].Serial != x.Serial {
			changed = true
		}
	}
	if !changed {
		return Result{"ok": true, "changed": false, "queue_position": s.QueuePosition(m)}, nil, nil
	}
	m.QueuePriority = nextPriority
	m.QueueBy = l.ID
	m.QueueChangedAt = now
	m.QueueChangedSerial = s.Serial + 1
	for i, x := range q {
		if x.Serial == m.Serial {
			q[i] = m
		}
	}
	s.setQueueOrder(l.ID, q, l.ID, now)
	evs := s.queueEvents(q, l.ID, "message.queue_changed", m.Serial)
	s.finish(&evs, now)
	return Result{"ok": true, "changed": true, "queue_position": s.QueuePosition(m)}, evs, nil
}

func (s *State) applyQueuePermission(op *Op, now time.Time) (Result, []Event, error) {
	by := op.PermissionActor
	if by != "" && by != HumanActor {
		a := s.Agents[by]
		if a == nil || a.CreatedSerial != op.PermissionActorCreated || !a.IsCoordinator() {
			return nil, nil, errf("E_NOT_PERMITTED", "queue policies require a human, coordinator or admin", "actor cannot change queue policy")
		}
	}
	owner := s.Agents[op.To]
	if owner == nil {
		return nil, nil, errf("E_NO_AGENT", "name an agent from the board", "unknown queue owner")
	}
	grant := op.Kind == OpGrantPermission
	if op.MsgSerial == 0 {
		// Shared permission mutation: queue policy never grants any other capability.
		res, evs, err := s.applyPermissionUnscoped(op, now)
		for i := range evs {
			evs[i].Data["by"] = by
		}
		return res, evs, err
	}
	m := s.Messages[op.MsgSerial]
	if m == nil || m.To != owner.ID || m.State != MsgStateQueued {
		return nil, nil, errf("E_NO_MESSAGE", "choose a queued request owned by the named agent", "not a queued task")
	}
	if m.QueueOrderLocked == grant {
		return Result{"ok": true, "changed": false, "held": grant}, nil, nil
	}
	m.QueueOrderLocked = grant
	m.QueueLockBy = by
	evs := []Event{{Type: "agent.permission_changed", Agent: owner.ID, To: m.From, Data: map[string]any{"permission": PermQueueOrderLock, "held": grant, "msg_serial": m.Serial, "by": by}}}
	s.finish(&evs, now)
	return Result{"ok": true, "changed": true, "held": grant}, evs, nil
}

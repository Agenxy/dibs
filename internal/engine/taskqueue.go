package engine

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

func owedCall(m *core.Message) string {
	return fmt.Sprintf("when finished: respond(msg_serial:%d, disposition:\"done\", body:...); "+
		"report steps with disposition:\"progress\"", m.Serial)
}

func (e *Engine) owedWorkView(agent string, now time.Time) []core.Result {
	var out []core.Result
	for _, m := range e.obligationsOf(agent, now) {
		r := core.Result{"msg_serial": m.Serial, "from": m.From, "from_name": e.agentName(m.From),
			"state": m.State, "completion": owedCall(m)}
		if m.State == core.MsgStateQueued {
			r["start"] = fmt.Sprintf("when you choose to start: respond(msg_serial:%d, disposition:\"approve\")", m.Serial)
		}
		out = append(out, r)
	}
	return out
}

// SetQueueOrderLock authenticates inside the writer loop and fixes the permission
// vocabulary before entering the existing tokenless permission mutation. No token
// bearing system op is accepted, and callers cannot supply the recorded actor.
func (e *Engine) SetQueueOrderLock(
	ctx context.Context, token, agent string, serial uint64, locked bool,
) (core.Result, error) {
	var mutationErr error
	res, err := e.query(ctx, func() core.Result {
		now := time.Now()
		actor, refused := e.authRead(token, now)
		if refused != nil {
			return refused
		}
		if !actor.IsCoordinator() {
			return core.Result{"error": &core.Error{
				Code: "E_NOT_PERMITTED", Msg: "queue policy needs a coordinator or admin",
				Hint: "ask the human or coordinator to change the scoped queue_order_lock permission",
			}}
		}
		kind := core.OpRevokePermission
		if locked {
			kind = core.OpGrantPermission
		}
		op := &core.Op{
			Kind: kind, To: agent, Mode: core.PermQueueOrderLock, MsgSerial: serial,
			PermissionActor: actor.ID, PermissionActorCreated: actor.CreatedSerial,
		}
		// This op is built here rather than arriving through exec, so the
		// name resolution exec does at ingress has to be done here too.
		addressed, refErr := e.resolveAgentRefs(op)
		if mutationErr = refErr; mutationErr != nil {
			return nil
		}
		if mutationErr = e.state.Admit(op); mutationErr != nil {
			return nil
		}
		r, applyErr := e.applyAndLedger(op, now)
		mutationErr = applyErr
		if r != nil && len(addressed) > 0 {
			r["addressed"] = e.addressedNote(op, addressed)
		}
		return r
	})
	if err != nil {
		return nil, err
	}
	return res, mutationErr
}

// SetQueueOrderLockByHuman is used only behind the existing HTTP admin gate.
func (e *Engine) SetQueueOrderLockByHuman(
	ctx context.Context, agent string, serial uint64, locked bool,
) (core.Result, error) {
	kind := core.OpRevokePermission
	if locked {
		kind = core.OpGrantPermission
	}
	return e.Do(ctx, &core.Op{Kind: kind, To: agent, Mode: core.PermQueueOrderLock, MsgSerial: serial})
}

func (e *Engine) taskQueueView(agent string) []core.Result {
	var out []core.Result
	owner := e.state.Agents[agent]
	now := time.Now()
	for i, m := range e.state.TaskQueue(agent) {
		r := core.Result{
			"msg_serial": m.Serial, "from": m.From, "from_name": e.agentName(m.From),
			"position": i + 1, "priority": m.EffectivePriority(),
			"sender_priority": m.RequestPriority, "deadline": m.Deadline,
			"overdue": queueOverdue(m, now) > 0, "overdue_s": queueOverdue(m, now).Seconds(),
			"locked": m.QueueOrderLocked || (owner != nil && owner.HasPermission(core.PermQueueOrderLock)),
		}
		if m.QueueBy != "" {
			r["by"] = m.QueueBy
			r["by_name"] = e.agentName(m.QueueBy)
		}
		out = append(out, r)
	}
	return out
}

func (e *Engine) prepareQueueDebt(op *core.Op) error {
	if op.Kind == core.OpRespond && (op.Disposition == "queue" || op.Disposition == "approve") {
		m := e.state.Messages[op.MsgSerial]
		op.QueueDebt = m != nil && m.Type == core.MsgRequest && m.Grant == "" && m.Adopt == "" && m.From != m.To
		if op.QueueDebt && !m.QueueDebt && e.state.AcceptedDebtCount(m.To) >= e.state.Limits.MaxMailboxDepth {
			return &core.Error{
				Code: "E_MAILBOX_FULL", Msg: "accepted work capacity reached",
				Hint: "complete or decline owed work before accepting more",
			}
		}
	}
	if op.Kind == core.OpSendMessage {
		op.QueueDebt = true
	} // capacity decision is ledgered; finishSend does not mark pending mail owed
	return nil
}

// rebuildQueueNotices reconstructs ordering news from replayed state, not the
// bounded event ring. It stays nonblocking and never starts the recipient.
func (e *Engine) rebuildQueueNotices() {
	if e.state == nil {
		return
	}
	var changed []*core.Message
	for _, m := range e.state.Messages {
		asker := e.state.Agents[m.From]
		if m.State == core.MsgStateQueued && asker != nil && !asker.Retired() &&
			m.QueueChangedSerial > m.RespondedAt && m.QueueChangedSerial > m.OutcomeReadAt &&
			m.QueueChangedSerial > asker.AckedSerial {
			changed = append(changed, m)
		}
	}
	sort.Slice(changed, func(i, j int) bool { return changed[i].QueueChangedSerial < changed[j].QueueChangedSerial })
	for _, m := range changed {
		e.noteEvent(core.Event{
			Type: "message.queue_changed", Agent: m.QueueBy, To: m.From,
			Serial: m.QueueChangedSerial, TS: m.QueueChangedAt,
			Data: map[string]any{"msg_serial": m.Serial},
		})
	}
}

// queueMessageView projects live rank and inherited locks without changing
// replayable message fields (adoption can merge queues with equal stored ranks).
func (e *Engine) queueMessageView(m *core.Message) *core.Message {
	if m.State != core.MsgStateQueued {
		return m
	}
	view := *m
	view.QueueRank = e.state.QueuePosition(m)
	if owner := e.state.Agents[m.To]; owner != nil && owner.HasPermission(core.PermQueueOrderLock) {
		view.QueueOrderLocked = true
	}
	return &view
}

func queueOverdue(m *core.Message, now time.Time) time.Duration {
	if m.Deadline.IsZero() || !now.After(m.Deadline) {
		return 0
	}
	return now.Sub(m.Deadline)
}

func (e *Engine) addQueueCheckpoint(res core.Result, actor *core.Agent, op *core.Op, now time.Time) {
	if res == nil || actor == nil || op.Kind != core.OpRespond {
		return
	}
	switch op.Disposition {
	case "queue", "approve", "done", "decline":
		res["task_queue"] = e.taskQueueView(actor.ID)
		res["owed_work"] = e.owedWorkView(actor.ID, now)
		res["owes"] = e.owedSerials(actor.ID, now)
	}
}

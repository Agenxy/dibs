package engine

import (
	"context"
	"fmt"
	"github.com/agenxy/dibs/internal/core"
	"time"
)

func owedCall(m *core.Message) string {
	return fmt.Sprintf("when finished: respond(msg_serial:%d, disposition:\"done\", body:...); report steps with disposition:\"progress\"", m.Serial)
}

func (e *Engine) owedWorkView(agent string, now time.Time) []core.Result {
	var out []core.Result
	for _, m := range e.obligationsOf(agent, now) {
		r := core.Result{"msg_serial": m.Serial, "from": m.From, "state": m.State, "completion": owedCall(m)}
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
func (e *Engine) SetQueueOrderLock(ctx context.Context, token, agent string, serial uint64, locked bool) (core.Result, error) {
	var mutationErr error
	res, err := e.query(ctx, func() core.Result {
		now := time.Now()
		actor, refused := e.authRead(token, now)
		if refused != nil {
			return refused
		}
		if !actor.IsCoordinator() {
			return core.Result{"error": &core.Error{Code: "E_NOT_PERMITTED", Msg: "queue policy needs a coordinator or admin", Hint: "ask the human or coordinator to change the scoped queue_order_lock permission"}}
		}
		kind := core.OpRevokePermission
		if locked {
			kind = core.OpGrantPermission
		}
		op := &core.Op{Kind: kind, To: agent, Mode: core.PermQueueOrderLock, MsgSerial: serial, PermissionActor: actor.ID, PermissionActorCreated: actor.CreatedSerial}
		if mutationErr = core.Admit(op, e.state.Limits); mutationErr != nil {
			return nil
		}
		r, applyErr := e.applyAndLedger(op, now)
		mutationErr = applyErr
		return r
	})
	if err != nil {
		return nil, err
	}
	return res, mutationErr
}

// SetQueueOrderLockByHuman is used only behind the existing HTTP admin gate.
func (e *Engine) SetQueueOrderLockByHuman(ctx context.Context, agent string, serial uint64, locked bool) (core.Result, error) {
	kind := core.OpRevokePermission
	if locked {
		kind = core.OpGrantPermission
	}
	return e.Do(ctx, &core.Op{Kind: kind, To: agent, Mode: core.PermQueueOrderLock, MsgSerial: serial})
}

func (e *Engine) taskQueueView(agent string) []core.Result {
	var out []core.Result
	owner := e.state.Agents[agent]
	for i, m := range e.state.TaskQueue(agent) {
		r := core.Result{"msg_serial": m.Serial, "from": m.From, "position": i + 1, "priority": m.EffectivePriority(), "sender_priority": m.RequestPriority, "deadline": m.Deadline, "overdue": !m.Deadline.IsZero() && time.Now().After(m.Deadline), "locked": m.QueueOrderLocked || (owner != nil && owner.HasPermission(core.PermQueueOrderLock))}
		if m.QueueBy != "" {
			r["by"] = m.QueueBy
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
			return &core.Error{Code: "E_MAILBOX_FULL", Msg: "accepted work capacity reached", Hint: "complete or decline owed work before accepting more"}
		}
	}
	if op.Kind == core.OpSendMessage {
		op.QueueDebt = true
	} // capacity decision is ledgered; finishSend does not mark pending mail owed
	return nil
}

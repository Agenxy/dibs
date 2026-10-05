package engine

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/notify"
)

// NotificationCleanup is an additive relay envelope. The notice's top-level
// serial stays zero, so old relay readers ignore rather than present it.
type NotificationCleanup struct {
	Node    string   `json:"node"`
	Serials []uint64 `json:"serials"`
}

type humanNotificationRemover interface {
	RemoveMessages(string, []uint64) notify.Cleanup
}

func humanDecision(m *core.Message) bool {
	if m == nil || (m.Type != core.MsgQuestion && m.Type != core.MsgRequest) {
		return false
	}
	switch m.State {
	case core.MsgStateWithdrawn, core.MsgStateAnswered, core.MsgStateApproved,
		core.MsgStateDenied, core.MsgStateDeclined, core.MsgStateDone:
		return true
	}
	return false
}

func (e *Engine) startHumanCleanup(ctx context.Context) {
	jobs := make(chan NotificationCleanup, notify.CleanupBatch)
	e.humanDelivery.mu.Lock()
	e.humanDelivery.cleanup = jobs
	n := e.humanDelivery.notifier
	e.humanDelivery.mu.Unlock()
	remove := notify.RemoveMessages
	if n != nil {
		if cleaner, ok := n.(humanNotificationRemover); ok {
			remove = cleaner.RemoveMessages
		} else {
			remove = func(string, []uint64) notify.Cleanup {
				return notify.Cleanup{State: "unsupported", BestEffort: true, Error: "configured notifier has no cleanup route"}
			}
		}
	}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case batch := <-jobs:
				result := remove(batch.Node, batch.Serials)
				for _, serial := range batch.Serials {
					e.recordDesktopDelivery(serial, "cleanup_"+result.State, result.Error)
				}
			}
		}
	}()
}

// On the writer. No cleanup result changes replayable state.
func (e *Engine) requestHumanCleanup(serials []uint64) {
	if len(serials) == 0 || len(serials) > notify.CleanupBatch {
		return
	}
	batch := NotificationCleanup{Node: e.state.NodeID, Serials: append([]uint64(nil), serials...)}
	e.enqueueHumanNotice(HumanNotice{Cleanup: &batch})
	e.humanDelivery.mu.Lock()
	jobs := e.humanDelivery.cleanup
	e.humanDelivery.mu.Unlock()
	select {
	case jobs <- batch:
	default:
		for _, serial := range serials {
			e.recordDesktopDelivery(serial, "cleanup_failed", "notification cleanup queue is full or unavailable")
		}
	}
}

func (e *Engine) noteHumanCleanup(evs []core.Event) {
	human := e.humanIdentityLocked()
	if human == "" {
		return
	}
	for _, ev := range evs {
		switch ev.Type {
		case "message.withdrawn", "message.answered", "message.approved",
			"message.denied", "message.declined", "message.done":
		default:
			continue
		}
		serial, _ := ev.Data["msg_serial"].(uint64)
		m := e.state.Messages[serial]
		if m != nil && m.To == human && humanDecision(m) {
			e.requestHumanCleanup([]uint64{serial})
		}
	}
}

func (e *Engine) cleanupLateHumanPost(serial uint64) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := e.query(ctx, func() core.Result {
		m := e.state.Messages[serial]
		if m != nil && m.To == e.humanIdentityLocked() && humanDecision(m) {
			e.requestHumanCleanup([]uint64{serial})
		}
		return nil
	})
	if err != nil {
		e.recordDesktopDelivery(serial, "cleanup_failed", fmt.Sprintf("late posting check: %v", err))
	}
}

// On the writer: bounded to newest retained outcomes, and one batched job.
func (e *Engine) humanCleanupAt(now time.Time) []uint64 {
	human := e.humanIdentityLocked()
	var serials []uint64
	for serial, m := range e.state.Messages {
		if human != "" && m.To == human && humanDecision(m) &&
			!m.RetainUntil.IsZero() && m.RetainUntil.After(now) {
			serials = append(serials, serial)
		}
	}
	sort.Slice(serials, func(i, j int) bool { return serials[i] > serials[j] })
	if len(serials) > notify.CleanupBatch {
		serials = serials[:notify.CleanupBatch]
	}
	return serials
}

// HumanCleanupForRelay derives one capped reconnect batch from retained outcomes.
func (e *Engine) HumanCleanupForRelay(ctx context.Context) (*NotificationCleanup, error) {
	var batch *NotificationCleanup
	_, err := e.query(ctx, func() core.Result {
		serials := e.humanCleanupAt(time.Now())
		if len(serials) != 0 {
			batch = &NotificationCleanup{Node: e.state.NodeID, Serials: serials}
		}
		return nil
	})
	return batch, err
}

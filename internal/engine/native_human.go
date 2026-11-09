// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// NativeHumanView is current daemon state, not the notification's saved copy.
type NativeHumanView struct {
	Notice  HumanNotice `json:"notice"`
	State   string      `json:"state"`
	Pending bool        `json:"pending"`
	Media   []MediaInfo `json:"media,omitempty"`
}

func (e *Engine) nativeHumanViewLocked(node string, serial uint64, now time.Time) (NativeHumanView, error) {
	if node != e.state.NodeID {
		return NativeHumanView{}, nativeHumanError("E_WRONG_BOARD", "notification belongs to another board")
	}
	m := e.state.Messages[serial]
	if m == nil || m.To != e.humanIdentityLocked() {
		return NativeHumanView{}, ErrNotTheHumans
	}
	pending, state := nativeHumanPending(m, now)
	return NativeHumanView{Notice: e.noticeOf(m), State: state, Pending: pending, Media: e.mediaOfLocked(m)}, nil
}

// nativeHumanPending is the one rule for whether the window may still answer.
func nativeHumanPending(m *core.Message, now time.Time) (bool, string) {
	state := m.State
	pending := state == core.MsgStatePending || state == core.MsgStateDelivered
	if pending && !m.Deadline.IsZero() && !now.Before(m.Deadline) {
		pending, state = false, "expired"
	}
	return pending, state
}

// NativeHumanQueue places one message among the human's answerable mail,
// oldest first, so a window the person opened walks on to the rest of it
// instead of the person clicking every banner in turn. Next and Previous
// wrap around and are zero when nothing else is waiting.
type NativeHumanQueue struct {
	Position int    `json:"position"` // 1-based; 0 once this message is settled
	Total    int    `json:"total"`
	Next     uint64 `json:"next,omitempty"`
	Previous uint64 `json:"previous,omitempty"`
}

// NativeHumanQueue reads only human-addressed questions and requests, after
// transport attestation, and never another board's.
func (e *Engine) NativeHumanQueue(ctx context.Context, node string, serial uint64) (NativeHumanQueue, error) {
	var q NativeHumanQueue
	_, err := e.query(ctx, func() core.Result {
		if node != e.state.NodeID {
			return nil
		}
		now, human := time.Now(), e.humanIdentityLocked()
		var open []uint64
		for _, m := range e.state.Messages {
			if m.To != human || (m.Type != core.MsgQuestion && m.Type != core.MsgRequest) {
				continue
			}
			if pending, _ := nativeHumanPending(m, now); pending {
				open = append(open, m.Serial)
			}
		}
		q = queueAround(open, serial)
		return nil
	})
	return q, err
}

func queueAround(open []uint64, serial uint64) NativeHumanQueue {
	slices.Sort(open)
	q := NativeHumanQueue{Total: len(open)}
	i, found := slices.BinarySearch(open, serial)
	others := len(open)
	after := i
	if found {
		q.Position, others, after = i+1, others-1, i+1
	}
	if others == 0 {
		return q
	}
	q.Next, q.Previous = open[0], open[len(open)-1]
	if after < len(open) {
		q.Next = open[after]
	}
	if i > 0 {
		q.Previous = open[i-1]
	}
	return q
}

// NativeHumanMessage reads only human-addressed mail, after transport attestation.
func (e *Engine) NativeHumanMessage(ctx context.Context, node string, serial uint64) (NativeHumanView, error) {
	var view NativeHumanView
	var fault error
	_, err := e.query(ctx, func() core.Result {
		view, fault = e.nativeHumanViewLocked(node, serial, time.Now())
		return nil
	})
	if err != nil {
		return NativeHumanView{}, err
	}
	return view, fault
}

// AnswerNativeHuman rechecks pending state and submits on the same writer trip.
// No waiter, helper lifetime, or requester process owns the ability to reply.
// commentID is idempotency data, never a credential; comments keep a request open.
func (e *Engine) AnswerNativeHuman(
	ctx context.Context, node string, serial uint64, disposition, body, commentID string,
) error {
	plan, err := e.planDesktopApproval(ctx, node, serial, disposition, body, commentID)
	if err != nil {
		return err
	}
	if err := e.confirmDesktopApproval(ctx, &plan); err != nil {
		return err
	}
	_, token, err := e.HumanAgent(ctx)
	if err != nil {
		return err
	}
	r, err := e.query(ctx, func() core.Result {
		now := time.Now()
		view, fault := e.nativeHumanViewLocked(node, serial, now)
		if fault != nil {
			return core.Result{"error": fault}
		}
		if !view.Pending {
			return core.Result{"error": nativeHumanError("E_ALREADY_ANSWERED", "message is "+view.State)}
		}
		if fault := e.recheckDesktopApprovalLocked(plan, node, serial, disposition); fault != nil {
			return core.Result{"error": fault}
		}
		op, fault := nativeHumanResponseOp(view.Notice, token, serial, disposition, body, commentID)
		if fault != nil {
			return core.Result{"error": fault}
		}
		result, fault := e.exec(op, now)
		if fault != nil {
			return core.Result{"error": fault}
		}
		return result
	})
	if err != nil {
		return err
	}
	if fault, ok := r["error"].(error); ok {
		return fault
	}
	return nil
}

func nativeHumanResponseOp(
	n HumanNotice, token string, serial uint64, disposition, body, commentID string,
) (*core.Op, error) {
	op := &core.Op{Kind: core.OpRespond, Token: token, MsgSerial: serial, Disposition: disposition, Body: body}
	switch n.Type {
	case core.MsgQuestion:
		if disposition != "answer" || strings.TrimSpace(body) == "" {
			return nil, nativeHumanError("E_BAD_REPLY", "a question needs an answer")
		}
	case core.MsgRequest:
		if disposition == "comment" {
			if strings.TrimSpace(body) == "" || len(commentID) == 0 || len(commentID) > 64 {
				return nil, nativeHumanError("E_BAD_REPLY", "a comment needs text and a submission id")
			}
			op = &core.Op{
				Kind: core.OpSendMessage, Token: token, To: n.From, MsgType: core.MsgNotify,
				Body: fmt.Sprintf("Reply to request #%d (no decision):\n\n%s", serial, body), OpID: "native-comment:" + commentID,
			}
		} else if disposition != "approve" && disposition != "deny" {
			return nil, nativeHumanError("E_BAD_REPLY", "choose approve, deny or comment")
		}
	default:
		return nil, nativeHumanError("E_BAD_REPLY", "this message does not ask for a reply")
	}
	return op, nil
}

func nativeHumanError(code, text string) error {
	return &core.Error{
		Code: code, Msg: text,
		Hint: "read the current notification or use the Dibs board; nothing was changed",
	}
}

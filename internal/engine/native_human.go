// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// NativeHumanView is current daemon state, not the notification's saved copy.
type NativeHumanView struct {
	Notice  HumanNotice `json:"notice"`
	State   string      `json:"state"`
	Pending bool        `json:"pending"`
}

func (e *Engine) nativeHumanViewLocked(node string, serial uint64, now time.Time) (NativeHumanView, error) {
	if node != e.state.NodeID {
		return NativeHumanView{}, nativeHumanError("E_WRONG_BOARD", "notification belongs to another board")
	}
	m := e.state.Messages[serial]
	if m == nil || m.To != e.humanIdentityLocked() {
		return NativeHumanView{}, ErrNotTheHumans
	}
	state := m.State
	pending := state == core.MsgStatePending || state == core.MsgStateDelivered
	if pending && !m.Deadline.IsZero() && !now.Before(m.Deadline) {
		pending, state = false, "expired"
	}
	return NativeHumanView{Notice: e.noticeOf(m), State: state, Pending: pending}, nil
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
	if _, err := e.NativeHumanMessage(ctx, node, serial); err != nil {
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

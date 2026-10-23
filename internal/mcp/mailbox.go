// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"context"
	"encoding/json"

	"github.com/agenxy/dibs/internal/core"
)

func validateMailboxLimit(name string, params json.RawMessage, a *toolArgs) error {
	if (name == "inbox" || name == "check_in") && argumentPresent(params, "limit") && a.Limit < 1 {
		return &core.Error{Code: "E_BAD_ARG", Msg: "invalid mailbox limit", Hint: "choose limit between 1 and 8"}
	}
	return nil
}

func (s *Server) ackMail(ctx context.Context, a *toolArgs, op *core.Op) (core.Result, error) {
	if a.NotifyThroughSerial != 0 {
		op.Kind, op.MsgSerial, op.NotifyThroughSerial = core.OpAckMessage, a.MsgSerial, a.NotifyThroughSerial
		return s.eng.Do(ctx, op)
	}
	if a.MsgSerial == 0 {
		return nil, &core.Error{
			Code: "E_BAD_ARG", Msg: "ack needs a selection",
			Hint: "pass msg_serial from inbox or notify_through_serial from check_in, never both",
		}
	}
	op.Kind, op.MsgSerial = core.OpAckMessage, a.MsgSerial
	return s.eng.Do(ctx, op)
}

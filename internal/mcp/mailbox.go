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
	if a.MsgSerial == 0 && !a.SeenFYIs {
		return nil, &core.Error{Code: "E_BAD_ARG", Msg: "ack needs a selection", Hint: "pass msg_serial or seen_fyis:true"}
	}
	if a.SeenFYIs {
		if a.MsgSerial != 0 {
			return nil, &core.Error{Code: "E_BAD_ARG", Msg: "ack modes conflict", Hint: "choose msg_serial or seen_fyis:true"}
		}
		return s.eng.AckSeenFYIs(ctx, a.Token)
	}
	op.Kind, op.MsgSerial = core.OpAckMessage, a.MsgSerial
	return s.eng.Do(ctx, op)
}

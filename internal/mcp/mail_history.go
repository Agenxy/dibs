// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"context"
	"encoding/json"

	"github.com/agenxy/dibs/internal/core"
)

func (s *Server) readHistory(ctx context.Context, params json.RawMessage, a *toolArgs) (core.Result, error) {
	if argumentPresent(params, "limit") && a.Limit == 0 {
		return nil, &core.Error{
			Code: "E_HISTORY_ARGS", Msg: "limit must be between 1 and 100",
			Hint: "call mail_history with limit 1–100, or omit limit for the default",
		}
	}
	return s.eng.ReadMailHistory(ctx, a.Token, a.Since, a.Limit, a.IncludeBodies, a.HistoryCursor)
}

// Preserve the two bounded progress scalars when MCP marks a history refusal
// isError. Never copy units or arbitrary engine payload into an error frame.
func appendHistoryRefusal(name string, payload map[string]any, res core.Result) {
	if name != "mail_history" {
		return
	}
	for _, key := range []string{"as_of_serial", "behind_by"} {
		if value, ok := res[key].(uint64); ok {
			payload[key] = value
		}
	}
}

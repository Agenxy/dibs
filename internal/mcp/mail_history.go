package mcp

import (
	"context"
	"encoding/json"

	"github.com/agenxy/dibs/internal/core"
)

func (s *Server) readHistory(ctx context.Context, params json.RawMessage, a toolArgs) (core.Result, error) {
	if argumentPresent(params, "limit") && a.Limit == 0 {
		return nil, &core.Error{
			Code: "E_HISTORY_ARGS", Msg: "limit must be between 1 and 100",
			Hint: "call mail_history with limit 1–100, or omit limit for the default",
		}
	}
	return s.eng.ReadMailHistory(ctx, a.Token, a.Since, a.Limit, a.IncludeBodies, a.HistoryCursor)
}

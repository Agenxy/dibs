// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"encoding/json"

	"github.com/agenxy/dibs/internal/core"
)

// Before schema parsing or session adoption: even empty/null stale arguments
// are refused with the same corrective hint, without mutating coordination.
func removedRecheckRefusal(params json.RawMessage) map[string]any {
	var call toolCallParams
	if json.Unmarshal(params, &call) != nil || call.Name != "declare" || !argumentPresent(params, "recheck_after") {
		return nil
	}
	body, _ := json.Marshal(map[string]any{
		"code": "E_BAD_ARG", "message": "recheck_after was removed", "hint": core.RemovedRecheckHint,
	})
	return map[string]any{"isError": true, "content": []map[string]any{{"type": "text", "text": string(body)}}}
}

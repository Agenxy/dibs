package engine

import "github.com/agenxy/dibs/internal/core"

// Strip quoted text while keeping each original byte length and list count.
// This is bounded by the curated content projection, never a whole ledger op.
func stripHistoryText(value any) any {
	switch v := value.(type) {
	case core.Result:
		out := make(core.Result, len(v))
		for key, item := range v {
			if key == "text" {
				out[key] = ""
				out["truncated"] = true
				continue
			}
			if key == "truncated" && out[key] == true {
				continue
			}
			out[key] = stripHistoryText(item)
		}
		return out
	case []core.Result:
		out := make([]core.Result, 0, len(v))
		for _, item := range v {
			out = append(out, stripHistoryText(item).(core.Result))
		}
		return out
	default:
		return value
	}
}

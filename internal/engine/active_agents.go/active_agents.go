package engine

import (
	"context"

	"github.com/agenxy/dibs/internal/core"
)

// ActiveAgentCWDs returns the working directories of agents that still exist on
// the board. It is a read-only snapshot taken on the writer loop, so a scorer
// indexer can evict derived repository indexes without racing registration,
// reclaim, or replay. Stale and dormant agents remain included because both can
// resume without registering again; only terminal records release an index.
func (e *Engine) ActiveAgentCWDs(ctx context.Context) ([]string, error) {
	res, err := e.query(ctx, func() core.Result {
		return core.Result{"cwds": e.activeAgentCWDs()}
	})
	if err != nil {
		return nil, err
	}
	cwds, _ := res["cwds"].([]string)
	return cwds, nil
}

func (e *Engine) activeAgentCWDs() []string {
	cwds := make([]string, 0, len(e.state.Agents))
	for _, l := range e.state.Agents {
		if l.Agent == nil || l.Agent.CWD == "" {
			continue
		}
		switch l.Status {
		case core.StatusClosed, core.StatusArchived, core.StatusUnreachable:
			continue
		default:
			cwds = append(cwds, l.Agent.CWD)
		}
	}
	return cwds
}

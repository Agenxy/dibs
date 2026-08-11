package engine

import (
	"context"

	"github.com/agenxy/dibs/internal/core"
)

// Prune closes finished agents. Admin-only: it runs on the human's admin path,
// never from an agent token, because evicting a peer is not an agent's decision.
// Empty agent means every agent that is not live.
func (e *Engine) Prune(ctx context.Context, agent string) (core.Result, error) {
	return e.Do(ctx, &core.Op{Kind: core.OpPruneLane, To: agent})
}

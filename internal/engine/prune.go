package engine

import (
	"context"

	"github.com/agenxy/lanes/internal/core"
)

// Prune closes finished lanes. Admin-only: it runs on the human's admin path,
// never from a lane token, because evicting a peer is not an agent's decision.
// Empty lane means every lane that is not live.
func (e *Engine) Prune(ctx context.Context, lane string) (core.Result, error) {
	return e.Do(ctx, &core.Op{Kind: core.OpPruneLane, To: lane})
}

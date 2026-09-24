package engine

import (
	"context"
	"log/slog"

	"github.com/agenxy/dibs/internal/core"
)

// MergeAgents folds a forked row back into the seat it should have been.
//
// ADMIN, and the reason is stronger than configure's. This moves one agent's
// mail into another's mailbox. Getting the pair backwards sends a seat's
// history somewhere it does not belong, and there is no undo: the fold is
// ledgered and replay reproduces it. A coordinator running the fleet does not
// need this; the person who granted admin decided somebody could.
//
// The FOLD is tokenless, like prune's, so the gate is here. That split is
// deliberate rather than incidental: core stays the pure decision, and who is
// allowed to ask is a question about the caller, which core has no business
// knowing.
func (e *Engine) MergeAgents(ctx context.Context, token, from, into string) (core.Result, error) {
	if _, err := e.query(ctx, func() core.Result {
		l := e.state.AgentByToken(token)
		if l == nil {
			return core.Result{"error": core.ErrBadToken}
		}
		if !l.IsAdmin() {
			return core.Result{"error": core.ErrNotAdmin}
		}
		return core.Result{"agent": l.ID}
	}); err != nil {
		return nil, err
	}
	res, err := e.Do(ctx, &core.Op{Kind: core.OpMergeAgents, To: from, MergeInto: into})
	if err != nil {
		return nil, err
	}
	slog.Info("a forked agent was merged back into its seat",
		"from", from, "into", into,
		"messages", res["messages"], "claims", res["claims"], "spaces", res["spaces"])
	return res, nil
}

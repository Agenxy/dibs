package engine

import (
	"context"
	"log/slog"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// Resolve the daemon identity outside the writer, then recheck the request and
// work version ON the writer. Withdrawal, completion or progress while the
// reporter was scheduled must not publish an obsolete notice.
func (e *Engine) sendRequestStallNotice(agent string, request, version uint64, body string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, token, err := e.dibsAgent(ctx)
	if err != nil {
		return
	}
	result, err := e.query(ctx, func() core.Result {
		m, worker := e.state.Messages[request], e.state.Agents[agent]
		if m == nil || m.State != core.MsgStateApproved || worker == nil || worker.Retired() {
			return nil
		}
		now := time.Now()
		current, _, _ := summarizeSlots(e.workSlotsOf(worker, now))
		if current != version || m.StallNotifiedDeclaration == version {
			return nil
		}
		_, err := e.exec(&core.Op{
			Kind: core.OpStallNotified, Token: token, To: m.From,
			MsgSerial: request, DeclarationSerial: version, Body: body,
		}, now)
		return core.Result{"error": err}
	})
	if err == nil && result != nil {
		err, _ = result["error"].(error)
	}
	if err != nil {
		slog.Warn("could not report a stalled request", "agent", agent, "request", request, "err", err)
	}
}

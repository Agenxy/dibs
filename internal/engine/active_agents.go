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
func (e *Engine) ActiveAgentCWDs(ctx context.Context) ([]AgentPlace, error) {
	res, err := e.query(ctx, func() core.Result {
		return core.Result{"places": e.activeAgentPlaces()}
	})
	if err != nil {
		return nil, err
	}
	places, _ := res["places"].([]AgentPlace)
	return places, nil
}

// AgentPlace is where one live agent is: its working directory, and the
// machine that directory is on, "" for this one.
//
// THE MACHINE TRAVELS WITH THE DIRECTORY. Eviction used to receive the
// directory strings alone, and a path is only a path on one computer: an
// agent on machine B at /repo kept machine A's index at /repo alive after
// every agent of A had gone, an index B is refused (an index serves the
// machine it was shipped from and no other), and B's own shipment for that
// root was then refused because A still held the slot. The index that
// nobody could use survived, and the one somebody needed never arrived.
// Round fifty-seven of the pre-release review.
type AgentPlace struct {
	CWD  string
	Host string
}

func (e *Engine) activeAgentPlaces() []AgentPlace {
	places := make([]AgentPlace, 0, len(e.state.Agents))
	for _, l := range e.state.Agents {
		if l.Agent == nil || l.Agent.CWD == "" {
			continue
		}
		switch l.Status {
		case core.StatusClosed, core.StatusArchived, core.StatusUnreachable:
			continue
		default:
			places = append(places, AgentPlace{CWD: l.Agent.CWD, Host: e.remoteHostOf(l)})
		}
	}
	return places
}

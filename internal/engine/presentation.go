package engine

import (
	"context"

	"github.com/agenxy/dibs/internal/core"
)

// agentName is a presentation address, not a state key. Call it only on the
// writer loop; authentication, routing and replay continue to use the id.
func (e *Engine) agentName(id string) string {
	if a := e.state.Agents[id]; a != nil && a.Name != "" {
		return a.Name
	}
	return id
}

// AgentNames returns one coherent read-time address map for an outgoing
// surface. The map is derived and never copied into an op or the ledger.
func (e *Engine) AgentNames(ctx context.Context) (map[string]string, error) {
	result, err := e.query(ctx, func() core.Result {
		names := make(map[string]string, len(e.state.Agents))
		for id := range e.state.Agents {
			names[id] = e.agentName(id)
		}
		return core.Result{"names": names}
	})
	if err != nil {
		return nil, err
	}
	names, _ := result["names"].(map[string]string)
	return names, nil
}

package mcp

import (
	"context"

	"github.com/agenxy/dibs/internal/core"
)

// messagePresentation adds current addresses only at the outgoing boundary.
// Message itself is a ledger type: its from/to JSON fields must remain IDs.
type messagePresentation struct {
	*core.Message
	FromName string `json:"from_name"`
	ToName   string `json:"to_name"`
}

type eventPresentation struct {
	core.Event
	AgentName string `json:"agent_name,omitempty"`
	ToName    string `json:"to_name,omitempty"`
}

func (s *Server) presentNames(ctx context.Context, res core.Result) {
	if res == nil {
		return
	}
	_, one := res["message"].(*core.Message)
	_, many := res["messages"].([]*core.Message)
	_, inbox := res["inbox"].([]*core.Message)
	_, events := res["events"].([]core.Event)
	if !one && !many && !inbox && !events {
		return
	}
	names, err := s.eng.AgentNames(ctx)
	if err != nil {
		return // A closing engine does not turn a successful read into an error.
	}
	name := func(id string) string {
		if current := names[id]; current != "" {
			return current
		}
		return id
	}
	wrap := func(m *core.Message) messagePresentation {
		return messagePresentation{Message: m, FromName: name(m.From), ToName: name(m.To)}
	}
	if one {
		res["message"] = wrap(res["message"].(*core.Message))
	}
	for _, key := range []string{"messages", "inbox"} {
		if ms, ok := res[key].([]*core.Message); ok {
			views := make([]messagePresentation, 0, len(ms))
			for _, m := range ms {
				views = append(views, wrap(m))
			}
			res[key] = views
		}
	}
	if es, ok := res["events"].([]core.Event); ok {
		views := make([]eventPresentation, 0, len(es))
		for _, ev := range es {
			views = append(views, eventPresentation{
				Event:     ev,
				AgentName: name(ev.Agent), ToName: name(ev.To),
			})
		}
		res["events"] = views
	}
}

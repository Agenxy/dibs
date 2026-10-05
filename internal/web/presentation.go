package web

import "github.com/agenxy/dibs/internal/core"

// Presentation fields are additive. Event and Message remain stable-id ledger
// objects; the current address is resolved only when the human view is read.
type eventPresentation struct {
	core.Event
	AgentName string `json:"agent_name,omitempty"`
	ToName    string `json:"to_name,omitempty"`
}

type messagePresentation struct {
	*core.Message
	FromName  string `json:"from_name"`
	ToName    string `json:"to_name"`
	AdoptName string `json:"adopt_name,omitempty"`
}

func displayName(names map[string]string, id string) string {
	if id == "" {
		return ""
	}
	if name := names[id]; name != "" {
		return name
	}
	return id
}

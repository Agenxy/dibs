package engine

import (
	"log/slog"
	"slices"
)

// Presentation only. Role pins still require their configured credential
// fingerprint: a current-name owner must never inherit a former owner's role.
type configuredNameAddress struct {
	Name      string   `json:"name"`
	ID        string   `json:"id,omitempty"`
	Via       string   `json:"via"`
	Shadowed  []string `json:"shadowed_aliases,omitempty"`
	Ambiguous []string `json:"ambiguous,omitempty"`
}

func (e *Engine) noteConfiguredName(name, id string, ambiguous []string) {
	aliases := e.nameAliases.Candidates(e.state, name, true)
	if len(aliases) == 0 && id == "" {
		delete(e.configuredNames, name)
		return
	}
	view := configuredNameAddress{Name: name, ID: id, Via: "alias", Ambiguous: ambiguous}
	if current, _ := e.state.LiveAgentRef(name); current != "" {
		view.Via = "current"
		for _, aliasID := range aliases {
			if aliasID != current {
				view.Shadowed = append(view.Shadowed, aliasID)
			}
		}
	}
	if e.configuredNames == nil {
		e.configuredNames = map[string]configuredNameAddress{}
	}
	previous := e.configuredNames[name]
	if len(view.Shadowed) > 0 && (previous.ID != view.ID || !slices.Equal(previous.Shadowed, view.Shadowed)) {
		slog.Info("configured role name shadows former-name aliases; fingerprint still required",
			"name", name, "resolved_id", id, "shadowed_alias_ids", view.Shadowed)
	}
	e.configuredNames[name] = view
}

func (e *Engine) configuredNameView() []configuredNameAddress {
	names := make([]string, 0, len(e.configuredNames))
	for name := range e.configuredNames {
		names = append(names, name)
	}
	slices.Sort(names)
	var views []configuredNameAddress
	for _, name := range names {
		id, ambiguous := e.nameAliases.Resolve(e.state, name, true)
		e.noteConfiguredName(name, id, ambiguous)
		if view, ok := e.configuredNames[name]; ok &&
			(view.Via == "alias" || len(view.Shadowed) > 0 || len(view.Ambiguous) > 0) {
			views = append(views, view)
		}
	}
	return views
}

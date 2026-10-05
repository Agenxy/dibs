package core

import "slices"

// AgentNameAliases is a disposable projection of ledger-regenerated events,
// never part of State. The engine owns it on its single-writer loop. A row's
// creation serial fences the aliases of an earlier occupant of the same id.
type AgentNameAliases struct {
	owners   map[string]nameAliasOwner
	released map[string]nameAliasRelease // diagnostic only, never routing
}

type nameAliasRelease struct {
	id      string
	created uint64
}

type nameAliasOwner struct {
	created uint64
	names   map[string]bool
}

// NewAgentNameAliases rebuilds ownership from the complete regenerated history.
func NewAgentNameAliases(st *State, history []Event) *AgentNameAliases {
	n := &AgentNameAliases{owners: map[string]nameAliasOwner{}, released: map[string]nameAliasRelease{}}
	n.Observe(st, history)
	return n
}

// Observe consumes only successfully committed events. Replay regenerates
// their name deltas from actual old/new row names, including historical ops.
func (n *AgentNameAliases) Observe(st *State, events []Event) {
	for _, ev := range events {
		if ev.Type != "agent.updated" || ev.Data == nil {
			continue
		}
		l := st.Agents[ev.Agent]
		created, ok := ev.Data["created_serial"].(uint64)
		if l == nil || !ok || created != l.CreatedSerial {
			continue
		}
		owner := n.owners[l.ID]
		if owner.created != created || owner.names == nil {
			owner = nameAliasOwner{created: created, names: map[string]bool{}}
		}
		previous, _ := ev.Data["renamed_from"].(string)
		current, _ := ev.Data["name"].(string)
		if previous != "" && previous != l.ID {
			owner.names[previous] = true
		}
		delete(owner.names, current)
		released, _ := ev.Data["release_names"].([]string)
		for _, name := range released {
			delete(owner.names, name)
			n.released[name] = nameAliasRelease{id: l.ID, created: created}
		}
		if len(owner.names) == 0 {
			delete(n.owners, l.ID)
		} else {
			n.owners[l.ID] = owner
		}
	}
}

// LastReleaser is a diagnostic only. A released name no longer routes to this
// agent, and a later current owner can legitimately receive mail sent to it.
func (n *AgentNameAliases) LastReleaser(st *State, name string) string {
	if n == nil {
		return ""
	}
	last := n.released[name]
	if l := st.Agents[last.id]; l != nil && l.CreatedSerial == last.created {
		return last.id
	}
	return ""
}

// Prune bounds the view by retained rows. Called on sweep/prune, not every
// mail op: collecting a derived view must not turn every send into a GC pass.
func (n *AgentNameAliases) Prune(st *State) {
	if n == nil {
		return
	}
	for id, owner := range n.owners {
		if l := st.Agents[id]; l == nil || l.CreatedSerial != owner.created {
			delete(n.owners, id)
		}
	}
	for name, release := range n.released {
		if l := st.Agents[release.id]; l == nil || l.CreatedSerial != release.created {
			delete(n.released, name)
		}
	}
}

// Names returns the sorted former labels retained by this row incarnation.
func (n *AgentNameAliases) Names(st *State, id string) []string {
	if n == nil {
		return nil
	}
	l := st.Agents[id]
	owner, ok := n.owners[id]
	if l == nil || !ok || owner.created != l.CreatedSerial {
		return nil
	}
	names := make([]string, 0, len(owner.names))
	for name := range owner.names {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// Candidates returns sorted alias owners under the existing eligibility rules.
func (n *AgentNameAliases) Candidates(st *State, ref string, liveOnly bool) []string {
	if n == nil || ref == "" {
		return nil
	}
	var live, retired []string
	for id, owner := range n.owners {
		l := st.Agents[id]
		if l == nil || l.CreatedSerial != owner.created || !owner.names[ref] {
			continue
		}
		if l.Gone() {
			retired = append(retired, id)
		} else {
			live = append(live, id)
		}
	}
	if len(live) == 0 && !liveOnly {
		live = retired
	}
	slices.Sort(live)
	return live
}

// Resolve delegates existing id/current-name eligibility and precedence to
// State. Only a missing result falls through to former names.
func (n *AgentNameAliases) Resolve(st *State, ref string, liveOnly bool) (string, []string) {
	var id string
	var ambiguous []string
	if liveOnly {
		id, ambiguous = st.LiveAgentRef(ref)
	} else {
		id, ambiguous = st.AgentRef(ref)
	}
	if id != "" || len(ambiguous) > 0 {
		return id, ambiguous
	}
	candidates := n.Candidates(st, ref, liveOnly)
	if len(candidates) == 1 {
		return candidates[0], nil
	}
	return "", candidates
}

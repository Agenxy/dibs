package core

import (
	"slices"
	"strings"
)

// MaxFormerNames bounds new additions, not historical replay. An agent can
// release old labels before another rename, without losing its mailbox.
const MaxFormerNames = 64

func admitReleaseNames(op *Op, lim Limits) error {
	if op.ReleaseNames == nil {
		return nil
	}
	if op.Kind != OpUpdate {
		return errf("E_BAD_ARG", "release_names belongs to update on your own identity", "release_names on %s", op.Kind)
	}
	if len(op.ReleaseNames) > MaxFormerNames {
		return errTooLarge("release_names", MaxFormerNames)
	}
	for _, name := range op.ReleaseNames {
		if strings.TrimSpace(name) == "" {
			return errf("E_BAD_ARG", "give each former name to release; omit release_names to keep them", "blank former name")
		}
		if len(name) > lim.MaxNameBytes {
			return errTooLarge("release_names", lim.MaxNameBytes)
		}
	}
	return nil
}

// AdmitNameChange is the pure state-dependent decision. The engine supplies
// the disposable ownership projection; no new validation enters Apply.
// It returns the releases that actually remove a name, in canonical order.
func AdmitNameChange(st *State, aliases *AgentNameAliases, l *Agent, op *Op) ([]string, error) {
	if l.Status == StatusClosed {
		return nil, errf("E_AGENT_CLOSED", "register a new identity with a new nonce", "agent %s is closed", l.ID)
	}
	if err := admitReleaseNames(op, st.Limits); err != nil {
		return nil, err
	}
	next := l.Name
	if op.Name != "" {
		next = op.Name
	}
	if len(next) > st.Limits.MaxNameBytes {
		return nil, errTooLarge("name", st.Limits.MaxNameBytes)
	}
	if next != l.Name {
		if err := st.nameIsAnotherAddress(op, l); err != nil {
			return nil, err
		}
		if other := st.siblingByName(next, l.ID); other != nil {
			return nil, errf("E_NAME_TAKEN", "pick another name, or leave name out to keep your existing label", "the name %q belongs to %s", next, other.ID)
		}
	}
	owned := map[string]bool{}
	for _, name := range aliases.Names(st, l.ID) {
		owned[name] = true
	}
	if next != l.Name && l.Name != l.ID {
		owned[l.Name] = true
	}
	delete(owned, next)
	var released []string
	for _, name := range op.ReleaseNames {
		if name == next || st.Agents[name] != nil {
			return nil, errf("E_BAD_ARG", "release a former label; current names and immutable ids stay addresses", "cannot release current name or id %q", name)
		}
		if owned[name] {
			released = append(released, name)
			delete(owned, name)
		} else if candidates := aliases.Candidates(st, name, false); len(candidates) > 0 && !slices.Contains(candidates, l.ID) {
			return nil, errf("E_NOT_PERMITTED", "release only a former name of your own identity", "%q is another agent's alias", name)
		}
	}
	if next != l.Name && len(owned) > MaxFormerNames {
		return nil, errf("E_TOO_LARGE", "release some former names with update(release_names) before adding another", "a rename would retain %d former names; limit %d", len(owned), MaxFormerNames)
	}
	slices.Sort(released)
	return released, nil
}

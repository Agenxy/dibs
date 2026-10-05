package core

import (
	"slices"
	"strings"
)

// MaxFormerNames bounds new additions, not historical replay. An agent can
// release old labels before another rename, without losing its mailbox.
const MaxFormerNames = 64

func (st *State) admitRegistrationName(op *Op) error {
	if op.Kind != OpRegister || strings.TrimSpace(op.Name) != "" {
		return nil
	}
	if op.Nonce != "" && st.Agents[st.Nonces[op.Nonce]] != nil {
		return nil
	}
	return errf("E_BAD_ARG", "supply name for a new identity; omit it only with your existing recovery nonce",
		"a new registration needs a name")
}

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
	if err := st.admitNextName(l, op, next); err != nil {
		return nil, err
	}
	owned := map[string]bool{}
	for _, name := range aliases.Names(st, l.ID) {
		owned[name] = true
	}
	if next != l.Name && l.Name != "" && l.Name != l.ID {
		owned[l.Name] = true
	}
	delete(owned, next)
	released, err := admitOwnedNameReleases(st, aliases, l, next, owned, op.ReleaseNames)
	if err != nil {
		return nil, err
	}
	if next != l.Name && len(owned) > MaxFormerNames {
		return nil, errf("E_TOO_LARGE", "release some former names with update(release_names) before adding another",
			"a rename would retain %d former names; limit %d", len(owned), MaxFormerNames)
	}
	return released, nil
}

func admitOwnedNameReleases(st *State, aliases *AgentNameAliases, l *Agent,
	next string, owned map[string]bool, names []string,
) ([]string, error) {
	var released []string
	for _, name := range names {
		if name == next || st.Agents[name] != nil {
			return nil, errf("E_BAD_ARG", "release a former label; current names and immutable ids stay addresses",
				"cannot release current name or id %q", name)
		}
		if owned[name] {
			released = append(released, name)
			delete(owned, name)
		} else if candidates := aliases.Candidates(st, name, false); len(candidates) > 0 &&
			!slices.Contains(candidates, l.ID) {
			return nil, errf("E_NOT_PERMITTED", "release only a former name of your own identity",
				"%q is another agent's alias", name)
		}
	}
	slices.Sort(released)
	return released, nil
}

func (st *State) admitNextName(l *Agent, op *Op, next string) error {
	if len(next) > st.Limits.MaxNameBytes {
		return errTooLarge("name", st.Limits.MaxNameBytes)
	}
	if next == l.Name {
		return nil
	}
	if err := st.nameIsAnotherAddress(op, l); err != nil {
		return err
	}
	if other := st.siblingByName(next, l.ID); other != nil {
		return errf("E_NAME_TAKEN", "pick another name, or leave name out to keep your existing label",
			"the name %q belongs to %s (currently named %q)", next, other.ID, other.Name)
	}
	return nil
}

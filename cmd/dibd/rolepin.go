package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// rolePins remembers WHICH agent a declared role was granted to.
//
// `[roles] admin = ["release-manager"]` authorises a STRING, and any agent may
// register under any free name. So an ordinary agent that could read dibs.toml,
// or guess the name, registered as `release-manager` and the reconciler handed
// it admin: the god view over every decrypted mailbox. Both SECURITY.md and
// docs/CONFIGURATION.md promised the opposite, "no agent can promote itself",
// and it did not have to promote itself: it only had to be called the right
// thing before the intended agent was. Reproduced on a live daemon before this
// was written. Present in v0.0.5 and v0.0.6.
//
// The pin closes the half that outlives a restart. The first agent granted a
// declared role has its credential fingerprinted here, and a later agent
// wearing the same name with a different credential is refused and logged.
// Taking the name back after the original is pruned stops working.
//
// It does NOT close first-registrant-wins on a genuinely fresh board, where
// there is nothing yet to compare against. That half is narrowed by the
// bounded grant window in roles.go rather than by this file, and the two are
// worth reading together.
//
// A file in the data directory, not the ledger: this is the daemon's own record
// of a decision it made, it must not be replayed into a board that is being
// rebuilt elsewhere, and adding a field to a ledgered op would freeze a wire
// name for something that is not coordination state.
type rolePins struct {
	path string
	// readErr is why the pins could not be read, so the refusal can say.
	readErr error
	// role -> declared name -> credential fingerprint
	Pins map[string]map[string]string `json:"pins"`
}

func loadRolePins(dir string) *rolePins {
	p := &rolePins{path: filepath.Join(dir, "roles.pinned"), Pins: map[string]map[string]string{}}
	b, err := os.ReadFile(p.path) // #nosec G304 -- the daemon's own data directory
	// ABSENT is a fresh board; UNREADABLE is a fault, and they must not look the
	// same. Treating every read error as "no pins yet" meant a permissions
	// problem, or a directory where the file should be, silently re-opened every
	// declared role to whoever held its name. Pin state that cannot be read has
	// to fail in the refusing direction. Raised by the pre-release review.
	if err != nil && !os.IsNotExist(err) {
		p.Pins = nil
		p.readErr = err
		return p
	}
	if err != nil {
		return p
	}
	// A damaged pin file must not silently un-pin every role: that is the
	// failure it exists to prevent, arriving as a corrupted read. Refusing to
	// grant is the safe direction, so the map stays empty and every declared
	// grant is treated as unverifiable until the operator removes the file.
	if err := json.Unmarshal(b, p); err != nil {
		p.Pins = nil
	}
	return p
}

// check reports whether this fingerprint may hold this declared role, and
// records it when the role is not pinned yet.
func (p *rolePins) check(role, name, fingerprint string) error {
	if p.Pins == nil {
		return fmt.Errorf("%s cannot be read (%v), so no declared role can be "+
			"verified: fix the file's permissions, or remove it to re-pin from the "+
			"agents holding these names now", p.path, p.readErr)
	}
	if fingerprint == "" {
		return fmt.Errorf("agent %q has no nonce, so it has no identity that survives "+
			"a restart and nothing here can be pinned to it. Register it with a nonce, "+
			"which is what makes it the same agent tomorrow", name)
	}
	byName := p.Pins[role]
	if byName == nil {
		byName = map[string]string{}
		p.Pins[role] = byName
	}
	switch pinned := byName[name]; pinned {
	case "":
		// PERSIST FIRST, then remember.
		//
		// Recording it in memory before saving meant a failed write left the
		// fingerprint sitting in the map: the first reconciliation refused
		// correctly, and fifteen seconds later the next one found its own
		// unsaved value, matched against it, and granted admin with no durable
		// pin behind it. A security decision that survives only in memory is one
		// that disappears on restart while behaving as though it did not.
		byName[name] = fingerprint
		if err := p.save(); err != nil {
			delete(byName, name)
			return fmt.Errorf("could not record which agent holds %s in %s (%w), so "+
				"the grant is refused: a pin that is not on disk is not a pin",
				role, p.path, err)
		}
		return nil
	case fingerprint:
		return nil
	default:
		return fmt.Errorf("the agent now called %q is not the one this board granted "+
			"%s to. A standing role follows an identity, not a name, and a name is "+
			"free for anyone to take once its holder is gone. If this is a deliberate "+
			"handover, remove %q from %s and it will pin to whoever holds the name next",
			name, role, name, p.path)
	}
}

func (p *rolePins) save() error {
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	// 0600: it names who holds admin on this board.
	return os.WriteFile(p.path, b, 0o600)
}

// unclaimed lists declared names that never received their role.
func (p *rolePins) unclaimed(c RolesConfig) map[string][]string {
	out := map[string][]string{}
	for role, names := range map[string][]string{
		"coordinator": c.Coordinator,
		"admin":       c.Admin,
	} {
		for _, n := range names {
			if n == "" {
				continue
			}
			if p.Pins == nil || p.Pins[role][n] == "" {
				out[role] = append(out[role], n)
			}
		}
	}
	return out
}

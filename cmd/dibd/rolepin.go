package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/agenxy/dibs/internal/engine"
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
		// AND KEEP THE REASON. Dropping it left `check` reporting that the file
		// "cannot be read (<nil>)", which tells an operator staring at a
		// refused grant nothing at all: the decision was safely fail-closed and
		// the one line explaining it was thrown away. readErr is what `check`
		// prints, so a parse failure belongs in it exactly as a read failure
		// does.
		p.readErr = err
	}
	// VALID JSON CAN STILL BE CORRUPTION. `{"pins":null}` unmarshals cleanly and
	// leaves the map nil, which fails closed correctly and then explained
	// itself as "cannot be read (<nil>)": the operator is told the file is
	// unreadable, given no reason, and sent to check permissions that are fine.
	// The structure is the fault, so the structure is what the error says.
	if p.readErr == nil && p.Pins == nil {
		p.readErr = errors.New("the file parsed but holds no pins object, so every " +
			"declared role would be treated as unverifiable. Delete it to re-pin " +
			"from the agents holding these names now")
	}
	return p
}

// check reports whether this fingerprint may hold this declared role, and
// records it when the role is not pinned yet.
// want is the fingerprint the OPERATOR named for this agent, from
// [roles.identity], or "" when they named none.
func (p *rolePins) check(role, name, fingerprint, want string) error {
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
	// THE OPERATOR PASTED THE NONCE ITSELF.
	//
	// A 256-bit nonce is 64 lowercase hex characters, which is exactly the shape
	// a fingerprint has, so no validator reading dibs.toml alone can tell them
	// apart and isFingerprint accepts both. Here both values exist, and that
	// makes the test exact rather than heuristic: hashing what the operator
	// wrote yields the agent's own fingerprint only if what they wrote was the
	// agent's own nonce.
	//
	// It matters far more than a mistyped setting. The nonce is the whole
	// recovery credential: anything running as the operator that can read the
	// file can register with that name and that nonce, be handed the agent's
	// token and mailbox, and inherit whatever role it holds. Refusing the grant
	// is not enough on its own, because the exposure has already happened, so
	// the error says to rotate rather than merely to correct.
	if want != "" && want != fingerprint && engine.RolePinFingerprint(want) == fingerprint {
		return fmt.Errorf("[roles.identity] holds agent %q's NONCE, not its "+
			"fingerprint. That is its whole recovery credential sitting in a "+
			"config file: anything running as you that reads dibs.toml can "+
			"register as %q with it and take its token, its mailbox and this "+
			"role. Give %q a new nonce, then put the fingerprint it reports "+
			"here instead. %s is refused until then", name, name, name, role)
	}
	byName := p.Pins[role]
	if byName == nil {
		byName = map[string]string{}
		p.Pins[role] = byName
	}
	switch pinned := byName[name]; pinned {
	case "":
		// FIRST-REGISTRANT-WINS IS NOT AUTHENTICATION.
		//
		// Pinning whoever showed up first held every LATER impostor to the
		// first one's identity and welcomed the first impostor without a
		// question. An agent that read dibs.toml, or simply guessed a likely
		// name, could register under it before the operator's own agent came
		// up and be granted admin: the god view, every agent's mail included.
		// The startup window made that a race rather than a standing offer,
		// which is not the same as making it safe. Found by the pre-release
		// review, which also noted that neither existing test could fail on
		// this path: one tested direct grant_role, and the other started from
		// an already-established legitimate pin.
		//
		// So the first pin must be one the operator can PROVE. They already
		// choose the agent's nonce; naming it in [roles.identity] is a secret
		// the config and the agent share and an impostor does not.
		if want == "" {
			// THE FINGERPRINT, NEVER THE NONCE.
			//
			// This said `<that agent's nonce>`, and an operator who followed it
			// wrote the raw credential into dibs.toml. A 256-bit nonce is 64 hex
			// characters, which is exactly the shape isFingerprint accepts, so
			// the file loaded, the board came up, and nothing ever said the
			// value was wrong. Any same-user process that read the file could
			// then register with that name AND that nonce and be handed the
			// agent's own token and mailbox: not a race any more, a standing
			// key. The line printed at startup (roles.go) was already correct,
			// so the two corrective messages contradicted each other and the
			// dangerous one came first.
			return fmt.Errorf("%q has no identity in [roles.identity], so the first "+
				"agent to register under that name would be granted %s on trust "+
				"alone. Add its FINGERPRINT under [roles.identity] in dibs.toml, "+
				"which %q prints at startup and `register` returns: never the nonce "+
				"itself, which is that agent's whole recovery credential and would "+
				"let anything that can read the file become it", name, role, name)
		}
		if want != fingerprint {
			// "the nonce [roles.identity] names" was the last place in this
			// release still describing that field as holding one. An operator
			// reading it while debugging a refused grant would reasonably go
			// and put the nonce there, which is the exposure the field was
			// changed to avoid.
			return fmt.Errorf("the agent registering as %q does not match the "+
				"fingerprint [roles.identity] names for it, so it is not the agent "+
				"you meant to grant %s to. Its own fingerprint is %s: if that is "+
				"the agent you mean, put THAT here, never its nonce",
				name, role, fingerprint)
		}
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
		// The repair takes BOTH files and a restart, and saying only half of it
		// sent the operator down a path that cannot work: pins are read once at
		// startup, so editing the pin file leaves the running reconciler on its
		// loaded copy, and a successor with the pin removed still fails against
		// the old [roles.identity] fingerprint on the next boot. An error that
		// names a corrective action which does not correct anything is worse
		// than one that names none.
		return fmt.Errorf("the agent now called %q is not the one this board granted "+
			"%s to. A standing role follows an identity, not a name, and a name is "+
			"free for anyone to take once its holder is gone. If this is a deliberate "+
			"handover it takes three steps, all of them: put the NEW agent's "+
			"fingerprint under [roles.identity] in dibs.toml, remove %q from %s, and "+
			"restart dibd. Both files are read at startup, so editing either one "+
			"under a running daemon changes nothing",
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

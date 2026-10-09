// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package boardconfig

import (
	"errors"
	"fmt"
	"strings"

	"github.com/BurntSushi/toml"
)

// RetiredWakeCooldown identifies one obsolete operator setting.
type RetiredWakeCooldown struct {
	Harness string
	Line    int
}

// Warning names the obsolete setting and the correction without blocking boot.
func (r RetiredWakeCooldown) Warning(path string) string {
	return fmt.Sprintf("[%s] cooldown in %s:%d was removed and has no effect: "+
		"Dibs no longer paces wakes; delete the line.", toml.Key{"wake", "exec", r.Harness}.String(), path, r.Line)
}

func isWakeCooldown(key toml.Key) bool {
	return len(key) == 4 && key[0] == "wake" && key[1] == "exec" && key[3] == "cooldown"
}

// FindWakeCooldowns uses the TOML decoder's positions, including quoted and
// dotted keys. A deliberate value decoder error exposes its public ParseError
// position; source grep cannot distinguish a real key from a comment or string.
func FindWakeCooldowns(b []byte) ([]RetiredWakeCooldown, error) {
	var locations struct {
		Wake struct {
			Exec map[string]struct{ Cooldown toml.Primitive }
		}
	}
	md, err := toml.Decode(string(b), &locations)
	if err != nil {
		return nil, err
	}
	var out []RetiredWakeCooldown
	for _, key := range md.Keys() {
		if !isWakeCooldown(key) {
			continue
		}
		var marker cooldownPosition
		err := md.PrimitiveDecode(locations.Wake.Exec[key[2]].Cooldown, &marker)
		var pos toml.ParseError
		if !errors.As(err, &pos) || pos.Message != errCooldownPosition.Error() {
			return nil, fmt.Errorf("locating retired %s: %w", key.String(), err)
		}
		// String positions point inside the quotes; multiline strings can start
		// on the following line. The assignment delimiter preceding this parsed
		// value locates the KEY's line rather than the first content line.
		prefix := string(b[:pos.Position.Start])
		assignment := strings.LastIndex(prefix, "=")
		if assignment < 0 {
			return nil, fmt.Errorf("cannot locate the assignment for %s; delete its cooldown key", key.String())
		}
		line := strings.Count(prefix[:assignment], "\n") + 1
		out = append(out, RetiredWakeCooldown{Harness: key[2], Line: line})
	}
	return out, nil
}

var errCooldownPosition = errors.New("retired cooldown position")

type cooldownPosition struct{}

func (*cooldownPosition) UnmarshalTOML(any) error { return errCooldownPosition }

func refuseWakeCooldown(b []byte) error {
	keys, err := FindWakeCooldowns(b)
	if err != nil {
		return err
	}
	if len(keys) > 0 {
		return removedWakeCooldown(keys[0].Harness)
	}
	return nil
}

func removedWakeCooldown(harness string) error {
	return fmt.Errorf("[%s] cooldown was removed: the receiving harness decides how to handle "+
		"mail during a busy turn. Remove the cooldown line; new mail is offered on its event, "+
		"and failed deliveries retain their bounded retry", toml.Key{"wake", "exec", harness}.String())
}

// RefuseWakeCooldownSetting gives controlled setting writers the same repair
// as controlled TOML writers; existing operator files use Load instead.
func RefuseWakeCooldownSetting(key string) error {
	if strings.HasPrefix(key, "wake.exec.") && strings.HasSuffix(key, ".cooldown") {
		harness := strings.TrimSuffix(strings.TrimPrefix(key, "wake.exec."), ".cooldown")
		return removedWakeCooldown(harness)
	}
	return nil
}

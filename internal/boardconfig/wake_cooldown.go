// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package boardconfig

import (
	"fmt"

	"github.com/BurntSushi/toml"
)

// Check presence before typed duration decoding, so empty, zero and mistyped
// values all get the same corrective refusal. This is a retired key, not an
// unknown key a joining client may ignore as a newer daemon's setting.
func refuseWakeCooldown(b []byte) error {
	var raw map[string]any
	md, err := toml.Decode(string(b), &raw)
	if err != nil {
		return err
	}
	for _, key := range md.Keys() {
		if len(key) == 4 && key[0] == "wake" && key[1] == "exec" && key[3] == "cooldown" {
			return removedWakeCooldown(key[2])
		}
	}
	return nil
}

func removedWakeCooldown(harness string) error {
	return fmt.Errorf("[%s] cooldown was removed: the receiving harness decides how to handle "+
		"mail during a busy turn. Remove the cooldown line; new mail is offered on its event, "+
		"and failed deliveries retain their bounded retry", toml.Key{"wake", "exec", harness}.String())
}

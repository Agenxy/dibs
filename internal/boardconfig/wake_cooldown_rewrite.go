// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package boardconfig

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/BurntSushi/toml"
)

// RemovedWakeCooldown is the exact source removed, using original line numbers.
type RemovedWakeCooldown struct {
	RetiredWakeCooldown
	Text string
}

// RemoveWakeCooldowns preserves every other byte, including comments and CRLF.
// Each deletion must parse to exactly the same TOML except the retired key.
// An inline table sharing its line with live settings is refused rather than
// guessing a rewrite that could discard the operator's other configuration.
func RemoveWakeCooldowns(b []byte) ([]byte, []RemovedWakeCooldown, error) {
	keys, err := FindWakeCooldowns(b)
	if err != nil || len(keys) == 0 {
		return b, nil, err
	}
	lines := strings.SplitAfter(string(b), "\n")
	var removed []RemovedWakeCooldown
	for i := len(keys) - 1; i >= 0; i-- {
		key := keys[i]
		var expected map[string]any
		if _, err := toml.Decode(strings.Join(lines, ""), &expected); err != nil {
			return nil, nil, err
		}
		wake, _ := expected["wake"].(map[string]any)
		exec, _ := wake["exec"].(map[string]any)
		harness, _ := exec[key.Harness].(map[string]any)
		delete(harness, "cooldown")
		start := key.Line - 1
		matched := false
		for end := start + 1; end <= len(lines); end++ {
			candidate := strings.Join(lines[:start], "") + strings.Join(lines[end:], "")
			var got map[string]any
			if _, err := toml.Decode(candidate, &got); err != nil || !reflect.DeepEqual(got, expected) {
				continue
			}
			removed = append(removed, RemovedWakeCooldown{key, strings.Join(lines[start:end], "")})
			lines = append(lines[:start:start], lines[end:]...)
			matched = true
			break
		}
		if !matched {
			section := toml.Key{"wake", "exec", key.Harness}.String()
			return nil, nil, fmt.Errorf("[%s] cooldown at line %d shares source with other settings; "+
				"delete only that key yourself, then run dibs upgrade again", section, key.Line)
		}
	}
	// Restore file order for the printed receipt, while deletion worked backwards.
	for i, j := 0, len(removed)-1; i < j; i, j = i+1, j-1 {
		removed[i], removed[j] = removed[j], removed[i]
	}
	return []byte(strings.Join(lines, "")), removed, nil
}

// WriteNew refuses retired keys in every config writer Dibs controls. Loading
// old operator files is deliberately a different operation: they must boot.
func WriteNew(dir string, b []byte) error {
	if err := refuseWakeCooldown(b); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "dibs.toml"), b, 0o600) // #nosec G304 -- operator's config directory
}

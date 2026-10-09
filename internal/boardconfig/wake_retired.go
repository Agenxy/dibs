// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package boardconfig

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// RetiredWakeSetting names an obsolete key at its original source line.
type RetiredWakeSetting struct {
	Key  toml.Key
	Line int
}

// Warning names the file and repair while allowing an old board to boot.
func (r RetiredWakeSetting) Warning(path string) string {
	if isWakeCooldown(r.Key) {
		return (RetiredWakeCooldown{Harness: r.Key[2], Line: r.Line}).Warning(path)
	}
	return fmt.Sprintf("[wake] open_app_after_idle in %s:%d was removed and has no effect: "+
		"Claude closed-session recovery opens immediately; delete the line.", path, r.Line)
}

func isWakeIdle(key toml.Key) bool {
	return len(key) == 2 && key[0] == "wake" && key[1] == "open_app_after_idle"
}

// FindRetiredWakeSettings uses parsed positions, never text matching comments.
func FindRetiredWakeSettings(b []byte) ([]RetiredWakeSetting, error) {
	cooldowns, err := FindWakeCooldowns(b)
	if err != nil {
		return nil, err
	}
	var locations struct {
		Wake struct {
			OpenAppAfterIdle toml.Primitive `toml:"open_app_after_idle"`
		}
	}
	md, err := toml.Decode(string(b), &locations)
	if err != nil {
		return nil, err
	}
	var out []RetiredWakeSetting
	for _, c := range cooldowns {
		out = append(out, RetiredWakeSetting{Key: toml.Key{"wake", "exec", c.Harness, "cooldown"}, Line: c.Line})
	}
	for _, key := range md.Keys() {
		if !isWakeIdle(key) {
			continue
		}
		var marker cooldownPosition
		err := md.PrimitiveDecode(locations.Wake.OpenAppAfterIdle, &marker)
		var pos toml.ParseError
		if !errors.As(err, &pos) || pos.Message != errCooldownPosition.Error() {
			return nil, fmt.Errorf("locating retired %s: %w", key.String(), err)
		}
		prefix := string(b[:pos.Position.Start])
		assignment := strings.LastIndex(prefix, "=")
		if assignment < 0 {
			return nil, fmt.Errorf("cannot locate assignment for %s; remove that key", key.String())
		}
		out = append(out, RetiredWakeSetting{Key: key, Line: strings.Count(prefix[:assignment], "\n") + 1})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Line < out[j].Line })
	return out, nil
}

// RemovedWakeSetting is the exact original source removed during migration.
type RemovedWakeSetting struct {
	RetiredWakeSetting
	Text string
}

// RemoveRetiredWakeSettings deletes only whole source spans whose parsed effect
// is exactly removal of one retired key. Mixed inline tables are refused.
func RemoveRetiredWakeSettings(b []byte) ([]byte, []RemovedWakeSetting, error) {
	keys, err := FindRetiredWakeSettings(b)
	if err != nil || len(keys) == 0 {
		return b, nil, err
	}
	return removeRetiredWakeSpans(b, keys)
}

func removeRetiredWakeSpans(b []byte, keys []RetiredWakeSetting) ([]byte, []RemovedWakeSetting, error) {
	lines := strings.SplitAfter(string(b), "\n")
	var removed []RemovedWakeSetting
	for i := len(keys) - 1; i >= 0; i-- {
		key := keys[i]
		var expected map[string]any
		if _, err := toml.Decode(strings.Join(lines, ""), &expected); err != nil {
			return nil, nil, err
		}
		parent := expected
		for _, part := range key.Key[:len(key.Key)-1] {
			parent, _ = parent[part].(map[string]any)
		}
		delete(parent, key.Key[len(key.Key)-1])
		start := key.Line - 1
		matched := false
		for end := start + 1; end <= len(lines); end++ {
			candidate := strings.Join(lines[:start], "") + strings.Join(lines[end:], "")
			var got map[string]any
			if _, err := toml.Decode(candidate, &got); err != nil || !reflect.DeepEqual(got, expected) {
				continue
			}
			removed = append(removed, RemovedWakeSetting{key, strings.Join(lines[start:end], "")})
			lines = append(lines[:start:start], lines[end:]...)
			matched = true
			break
		}
		if !matched {
			return nil, nil, fmt.Errorf("%s at line %d shares source with other settings; "+
				"delete only that key yourself, then run dibs upgrade again", key.Key.String(), key.Line)
		}
	}
	for i, j := 0, len(removed)-1; i < j; i, j = i+1, j-1 {
		removed[i], removed[j] = removed[j], removed[i]
	}
	return []byte(strings.Join(lines, "")), removed, nil
}

// RefuseRetiredWakeSetting is used by every controlled setting writer.
func RefuseRetiredWakeSetting(key string) error {
	if key == "wake.open_app_after_idle" {
		return errors.New("[wake] open_app_after_idle was removed: Claude closed-session recovery opens immediately. " +
			"Remove the open_app_after_idle line; no idle delay can be configured")
	}
	return RefuseWakeCooldownSetting(key)
}

func refuseRetiredWakeSettings(b []byte) error {
	keys, err := FindRetiredWakeSettings(b)
	if err != nil {
		return err
	}
	if len(keys) > 0 {
		return RefuseRetiredWakeSetting(strings.Join(keys[0].Key, "."))
	}
	return nil
}

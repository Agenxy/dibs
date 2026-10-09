// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package boardconfig

import (
	"os"
	"path/filepath"

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
	var settings []RetiredWakeSetting
	for _, key := range keys {
		settings = append(settings, RetiredWakeSetting{
			Key: toml.Key{"wake", "exec", key.Harness, "cooldown"}, Line: key.Line,
		})
	}
	after, spans, err := removeRetiredWakeSpans(b, settings)
	var removed []RemovedWakeCooldown
	for _, span := range spans {
		removed = append(removed, RemovedWakeCooldown{RetiredWakeCooldown{Harness: span.Key[2], Line: span.Line}, span.Text})
	}
	return after, removed, err
}

// WriteNew refuses retired keys in every config writer Dibs controls. Loading
// old operator files is deliberately a different operation: they must boot.
func WriteNew(dir string, b []byte) error {
	if err := refuseRetiredWakeSettings(b); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "dibs.toml"), b, 0o600) // #nosec G304 -- operator's config directory
}

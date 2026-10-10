// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/agenxy/dibs/internal/boardconfig"
)

func (p *plan) removeWakeCooldowns() error {
	if err := removeUpgradeWakeCooldowns(p.dir); err != nil {
		return fmt.Errorf("obsolete wake setting migration failed, so nothing has been stopped: %w", err)
	}
	return nil
}

// This runs on the cutover entry before any stop, including callers that did
// not construct a plan through the CLI. The old daemon remains serving if the
// backup, rewrite or rename fails. Only fixture configs are touched by tests.
func removeUpgradeWakeCooldowns(dir string) error {
	path := filepath.Join(dir, "dibs.toml")
	before, err := os.ReadFile(path) // #nosec G304 -- operator's board config
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	after, removed, err := boardconfig.RemoveRetiredWakeSettings(before)
	if err != nil || len(removed) == 0 {
		return err
	}
	info, err := os.Lstat(path) // #nosec G304 -- same config path
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o222 == 0 {
		return fmt.Errorf("%s is not a writable regular file; remove the obsolete wake setting lines yourself", path)
	}
	backup, err := os.CreateTemp(dir, "dibs.toml.before-cooldown-*")
	if err != nil {
		return err
	}
	if err := writeSyncedCooldownFile(backup, before); err != nil {
		return fmt.Errorf("backing up %s: %w", path, err)
	}
	say("backed up %s to %s", path, backup.Name())
	tmp, err := prepareCooldownReplacement(dir, after, info.Mode().Perm())
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp) }() //nolint:gosec // G703: CreateTemp sibling in the operator's config directory
	// Refuse operator edits observed during preparation before replacing bytes.
	current, err := os.ReadFile(path) // #nosec G304 -- same config path
	if err != nil || !bytes.Equal(current, before) {
		return fmt.Errorf("%s changed during migration; run dibs upgrade again (backup: %s)", path, backup.Name())
	}
	if err := os.Rename(tmp, path); err != nil { //nolint:gosec // G703: prepared sibling and operator's own config path
		return err
	}
	for _, key := range removed {
		say("removed [%s] %s from %s:%d: %s", key.Key[:len(key.Key)-1].String(), key.Key[len(key.Key)-1],
			path, key.Line, strings.TrimSuffix(key.Text, "\n"))
	}
	return nil
}

func writeSyncedCooldownFile(f *os.File, b []byte) error {
	defer func() { _ = f.Close() }()
	if _, err := f.Write(b); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	return f.Close()
}

func prepareCooldownReplacement(dir string, b []byte, mode os.FileMode) (path string, err error) {
	f, err := os.CreateTemp(dir, ".dibs.toml.no-cooldown-*")
	if err != nil {
		return "", err
	}
	defer func() {
		if err != nil {
			_ = f.Close()
			_ = os.Remove(f.Name())
		}
	}()
	if err = f.Chmod(mode); err != nil {
		return "", err
	}
	if err = writeSyncedCooldownFile(f, b); err != nil {
		return "", err
	}
	return f.Name(), nil
}

// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

func (p *codexPluginPlan) materialize() error {
	// #nosec G703 -- generated version root under operator's Codex home
	if _, err := os.Stat(p.source); err == nil {
		return p.verifyFiles(filepath.Join(p.source, "plugin"))
	} else if !os.IsNotExist(err) {
		return err
	}
	parent := filepath.Dir(p.source)
	if err := os.MkdirAll(parent, 0o700); err != nil { // #nosec G703 -- same version-root parent
		return err
	}
	tmp, err := os.MkdirTemp(parent, ".staging-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmp) }() // #nosec G703 -- own freshly created staging directory
	for name, body := range p.files {
		if !fs.ValidPath(name) {
			return fmt.Errorf("invalid embedded plugin path %q", name)
		}
		if err := writePluginFile(filepath.Join(tmp, "plugin", filepath.FromSlash(name)), []byte(body)); err != nil {
			return err
		}
	}
	marketplace := []byte(`{"name":"dibs","owner":{"name":"Agenxy"},` +
		`"plugins":[{"name":"dibs","source":"./plugin"}]}` + "\n")
	if err := writePluginFile(filepath.Join(tmp, ".agents", "plugins", "marketplace.json"), marketplace); err != nil {
		return err
	}
	return os.Rename(tmp, p.source) // #nosec G703 -- generated immutable version root, under operator's Codex home
}

func writePluginFile(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil { // #nosec G703 -- own generated/staging root
		return err
	}
	return os.WriteFile(path, b, 0o600) // #nosec G703 -- generated files below own staging root
}

func (p *codexPluginPlan) verifyFiles(root string) error {
	for name, want := range p.files {
		path := filepath.Join(root, filepath.FromSlash(name))
		info, err := os.Lstat(path) // #nosec G703 -- expected files below Codex's returned install root
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("plugin file is not regular: %s", path)
		}
		got, err := os.ReadFile(path) // #nosec G304 G703 -- same verified regular file
		if err != nil {
			return err
		}
		if !bytes.Equal(got, []byte(want)) {
			return fmt.Errorf("installed plugin bytes differ: %s", path)
		}
	}
	return nil
}

func (p *codexPluginPlan) current() bool {
	installed, err := installedCodexPlugin(p.home)
	if err != nil || !installed {
		return false
	}
	entries, err := os.ReadDir(p.cacheBase())
	if err != nil || len(entries) != 1 || entries[0].Name() != p.version || !entries[0].IsDir() {
		return false
	}
	// #nosec G304 G703 -- own verified-install receipt
	b, err := os.ReadFile(filepath.Join(p.home, "dibs-marketplace", "receipt.json"))
	var receipt struct{ Version string }
	return err == nil && json.Unmarshal(b, &receipt) == nil && receipt.Version == p.version &&
		p.verifyFiles(filepath.Join(p.cacheBase(), p.version)) == nil
}

func (p *codexPluginPlan) install(out io.Writer) (retErr error) {
	if p.current() {
		// #nosec G703 -- verified repair clears own failure marker
		_ = os.Remove(filepath.Join(p.home, "dibs-marketplace", "refresh-failed"))
		_, err := fmt.Fprintf(out, "Codex plugin already verified at %s; running chats retain their tool list.\n", p.version)
		return err
	}
	parent := filepath.Dir(p.source)
	if err := os.MkdirAll(parent, 0o700); err != nil { // #nosec G703 -- operator's Codex plugin state directory
		return err
	}
	lockPath := filepath.Join(parent, "install.lock")
	// #nosec G304 G703 -- local installer exclusion
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("plugin installer busy or lock unavailable (%s): %w; retry %s", lockPath, err, codexPluginRepair)
	}
	_ = lock.Close()
	defer func() { _ = os.Remove(lockPath) }() // #nosec G703 -- lock this call created
	if err := p.materialize(); err != nil {
		return err
	}
	rollback, cleanup, err := backupPluginRoot(p.cacheBase())
	if err != nil {
		return err
	}
	defer cleanup()
	defer func() {
		if retErr != nil {
			retErr = errors.Join(retErr, rollback())
		}
	}()
	b, err := runCodexPlugin(p.bin, p.args())
	if err != nil {
		return fmt.Errorf("codex plugin installer failed: %w (%s); run %s",
			err, string(b[:min(len(b), 2048)]), codexPluginRepair)
	}
	if err := p.verifyInstallReceipt(b); err != nil {
		return err
	}
	receipt, _ := json.Marshal(map[string]string{"version": p.version})
	if err := atomicPluginReceipt(parent, receipt); err != nil {
		return err
	}
	_ = os.Remove(filepath.Join(parent, "refresh-failed")) // #nosec G703 -- own diagnostic marker
	_, _ = fmt.Fprintf(out, "Codex plugin verified at %s. New chats/genuine app refreshes can load it; "+
		"running chats retain their tool list. Check dibs codex-hooks for hook trust.\n", p.version)
	return nil
}

func (p *codexPluginPlan) verifyInstallReceipt(b []byte) error {
	var result struct {
		PluginID      string `json:"pluginId"`
		Version       string `json:"version"`
		InstalledPath string `json:"installedPath"`
	}
	if err := json.Unmarshal(b, &result); err != nil {
		return fmt.Errorf("codex installer returned no verifiable JSON receipt: %w", err)
	}
	wantRoot := filepath.Join(p.cacheBase(), p.version)
	if result.PluginID != "dibs@dibs" || result.Version != p.version || filepath.Clean(result.InstalledPath) != wantRoot {
		return errors.New("codex installer returned a different plugin identity, version or root")
	}
	if err := p.verifyFiles(wantRoot); err != nil {
		return err
	}
	if installed, err := installedCodexPlugin(p.home); err != nil || !installed {
		return errors.New("codex installer did not leave dibs@dibs installed and enabled")
	}
	entries, err := os.ReadDir(p.cacheBase())
	if err != nil || len(entries) != 1 || entries[0].Name() != p.version {
		return errors.New("codex did not activate one unambiguous plugin version root")
	}
	return nil
}

func atomicPluginReceipt(dir string, b []byte) error {
	f, err := os.CreateTemp(dir, ".receipt-")
	if err != nil {
		return err
	}
	defer func() { _ = f.Close(); _ = os.Remove(f.Name()) }() // #nosec G703 -- own temporary receipt
	if _, err := f.Write(b); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	// #nosec G703 -- atomically publish own verified receipt
	return os.Rename(f.Name(), filepath.Join(dir, "receipt.json"))
}

// The Codex installer owns activation. Keep a byte snapshot so a success reply
// followed by failed verification cannot leave a half-written active plugin.
func backupPluginRoot(root string) (rollback func() error, cleanup func(), retErr error) {
	if err := os.MkdirAll(filepath.Dir(root), 0o700); err != nil { // #nosec G703 -- exact plugin cache parent
		return nil, nil, err
	}
	backup, err := os.MkdirTemp(filepath.Dir(root), ".dibs-backup-")
	if err != nil {
		return nil, nil, err
	}
	removeBackup := func() { _ = os.RemoveAll(backup) } // #nosec G703 -- own backup directory
	_, statErr := os.Lstat(root)                        // #nosec G703 -- Codex's exact Dibs plugin cache root
	if statErr != nil && !os.IsNotExist(statErr) {
		removeBackup()
		return nil, nil, statErr
	}
	existed := statErr == nil
	if existed {
		if err := copyPluginTree(root, filepath.Join(backup, "saved")); err != nil {
			removeBackup()
			return nil, nil, err
		}
	}
	rollback = func() error {
		failed := filepath.Join(backup, "failed")
		// #nosec G703 -- failed active root retained until restore
		if err := os.Rename(root, failed); err != nil && !os.IsNotExist(err) {
			removeBackup = func() {}
			return fmt.Errorf("plugin rollback failed; backup retained at %s: %w", backup, err)
		}
		if existed {
			// #nosec G703 -- restore exact prior plugin files
			if err := os.Rename(filepath.Join(backup, "saved"), root); err != nil {
				// Keep the snapshot if restoration failed; do not destroy the only copy.
				removeBackup = func() {}
				return fmt.Errorf("plugin rollback failed; backup retained at %s: %w", backup, err)
			}
		}
		return nil
	}
	// Close over cleanup so rollback can preserve the snapshot on failure.
	return rollback, func() { removeBackup() }, nil
}

func copyPluginTree(from, to string) error {
	source, err := os.OpenRoot(from)
	if err != nil {
		return err
	}
	defer func() { _ = source.Close() }()
	// #nosec G703 -- exact cache tree; symlinks refused, content reads confined by os.Root
	return filepath.WalkDir(from, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing plugin symlink %s", path)
		}
		rel, err := filepath.Rel(from, path)
		if err != nil {
			return err
		}
		dest := filepath.Join(to, rel)
		if entry.IsDir() {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			return os.MkdirAll(dest, info.Mode().Perm()) // #nosec G703 -- relative walk path in fresh backup root
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("refusing non-regular plugin file %s", path)
		}
		// Root-scoped reads keep a concurrent symlink replacement from escaping
		// the cache being snapshotted, even after WalkDir's metadata check.
		b, err := source.ReadFile(rel)
		if err != nil {
			return err
		}
		if err := writePluginFile(dest, b); err != nil {
			return err
		}
		return os.Chmod(dest, info.Mode().Perm()) // #nosec G703 -- preserve prior plugin permissions in snapshot
	})
}

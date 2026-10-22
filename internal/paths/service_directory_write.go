// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package paths

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// WriteServiceDirectory pins an explicitly selected absolute directory. The
// caller owns service migration/registration; this only replaces private config.
func WriteServiceDirectory(dir string) error {
	if !filepath.IsAbs(dir) {
		return errors.New("service directory must be absolute; preserve the existing board directory")
	}
	file, err := ServiceConfigPath()
	if err != nil {
		return err
	}
	var body bytes.Buffer
	if err := toml.NewEncoder(&body).Encode(struct {
		Version int    `toml:"version"`
		Dir     string `toml:"data_dir"`
	}{1, dir}); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(file), ".service-*.toml")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if _, err = f.Write(body.Bytes()); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	// CreateTemp gives mode 0600 and rename never follows an old symlink.
	return os.Rename(f.Name(), file)
}

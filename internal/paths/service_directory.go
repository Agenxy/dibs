// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package paths

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"github.com/BurntSushi/toml"
)

type DirectorySelection struct {
	Dir, Inherited, Source string
}

// ServiceConfigError is fatal to directory selection. A broken pin must never
// become an empty default board. Error-returning entry points use SelectDir;
// the legacy string-only accessors panic with this type rather than guess.
type ServiceConfigError struct {
	Path string
	Err  error
}

func (e *ServiceConfigError) Error() string {
	return fmt.Sprintf("cannot select the Dibs board from %s: %v; set DIBS_DIR to the existing board directory, then run dibs configure --service to repair its pin", e.Path, e.Err)
}

func (e *ServiceConfigError) Unwrap() error { return e.Err }

// ServiceConfigPath is independent of the data directory it pins.
func ServiceConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "Application Support", "org.agenxy.dibs", "service.toml"), nil
}

// SelectDir re-reads selection on every call. requirePin is the bundled
// service's startup rule, not a restriction on an unconfigured first CLI run.
func SelectDir(requirePin bool) (DirectorySelection, error) {
	if d := os.Getenv("DIBS_DIR"); d != "" {
		return DirectorySelection{Dir: d, Source: "DIBS_DIR"}, nil
	}
	if runtime.GOOS == "darwin" {
		file, err := ServiceConfigPath()
		if err != nil {
			return DirectorySelection{}, &ServiceConfigError{Path: "~/Library/Application Support/org.agenxy.dibs/service.toml", Err: err}
		}
		dir, found, err := serviceDirectory(file)
		if err != nil {
			return DirectorySelection{}, &ServiceConfigError{Path: file, Err: err}
		}
		if found {
			return DirectorySelection{Dir: dir, Source: file}, nil
		}
		if requirePin {
			return DirectorySelection{}, &ServiceConfigError{Path: file, Err: errors.New("required service pin is missing")}
		}
	}
	dir, inherited := defaultDirectory()
	source := "default"
	if inherited != "" {
		source = "legacy directory"
	}
	return DirectorySelection{Dir: dir, Inherited: inherited, Source: source}, nil
}

func serviceDirectory(file string) (string, bool, error) {
	info, err := os.Lstat(file)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return "", false, errors.New("service pin must be a regular private file (mode 0600)")
	}
	// #nosec G304 -- fixed user configuration path, never a participant path.
	f, err := os.Open(file)
	if err != nil {
		return "", false, err
	}
	defer func() { _ = f.Close() }()
	const limit = 64 << 10
	body, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || len(body) > limit {
		return "", false, errors.New("service pin is unreadable or exceeds 64 KiB")
	}
	var cfg struct {
		Version int    `toml:"version"`
		Dir     string `toml:"data_dir"`
	}
	meta, err := toml.Decode(string(body), &cfg)
	if err != nil {
		return "", false, err
	}
	if cfg.Version != 1 || !filepath.IsAbs(cfg.Dir) || len(meta.Undecoded()) != 0 {
		return "", false, errors.New("service pin requires version=1, an absolute data_dir and no unknown fields")
	}
	return cfg.Dir, true, nil
}

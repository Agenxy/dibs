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
	"strings"

	"github.com/BurntSushi/toml"
)

// DirectorySelection records where one board and its service settings came from.
type DirectorySelection struct {
	Dir, Inherited, Source         string
	ServicePath, ServicePathSource string
	ServicePathSet                 bool
}

// DefaultServicePath is the bundled service's fallback, never the CLI's PATH.
const DefaultServicePath = "/usr/bin:/bin:/usr/sbin:/sbin"

type serviceDirectoryConfig struct {
	Version    int     `toml:"version"`
	Dir        string  `toml:"data_dir"`
	Path       *string `toml:"service_path,omitempty"`
	PathSource string  `toml:"service_path_source,omitempty"`
}

// EffectiveServicePath describes only the bundled daemon's environment. CLI
// and bridge callers may inspect it, but must never apply it to themselves.
func (d DirectorySelection) EffectiveServicePath() (value, source string) {
	if d.ServicePathSet {
		return d.ServicePath, d.ServicePathSource
	}
	return DefaultServicePath, "default"
}

// ServiceConfigError is fatal to directory selection. A broken pin must never
// become an empty default board. Error-returning entry points use SelectDir;
// the legacy string-only accessors panic with this type rather than guess.
type ServiceConfigError struct {
	Path string
	Err  error
}

func (e *ServiceConfigError) Error() string {
	return fmt.Sprintf("cannot select the Dibs board from %s: %v; set DIBS_DIR to the "+
		"existing board directory, then run dibs configure --service to repair its pin", e.Path, e.Err)
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

// PinnedService inspects the fixed service configuration independently of the
// invoking CLI's DIBS_DIR override. Migration must never repoint a different
// board's service merely because the operator is inspecting a scratch board.
func PinnedService() (DirectorySelection, bool, error) {
	file, err := ServiceConfigPath()
	if err != nil {
		return DirectorySelection{}, false, &ServiceConfigError{Path: "~/Library/Application " +
			"Support/org.agenxy.dibs/service.toml", Err: err}
	}
	selected, found, err := serviceDirectory(file)
	if err != nil {
		return DirectorySelection{}, false, &ServiceConfigError{Path: file, Err: err}
	}
	selected.Source = file
	return selected, found, nil
}

// SelectDir re-reads selection on every call. requirePin is the bundled
// service's startup rule, not a restriction on an unconfigured first CLI run.
func SelectDir(requirePin bool) (DirectorySelection, error) {
	if d := os.Getenv("DIBS_DIR"); d != "" {
		return DirectorySelection{Dir: d, Source: "DIBS_DIR"}, nil
	}
	if runtime.GOOS == "darwin" {
		selected, found, err := PinnedService()
		if err != nil {
			return DirectorySelection{}, err
		}
		if found {
			return selected, nil
		}
		if requirePin {
			return DirectorySelection{}, &ServiceConfigError{
				Path: selected.Source, Err: errors.New("required service pin is missing"),
			}
		}
	}
	dir, inherited := defaultDirectory()
	source := "default"
	if inherited != "" {
		source = "legacy directory"
	}
	return DirectorySelection{Dir: dir, Inherited: inherited, Source: source}, nil
}

func serviceDirectory(file string) (DirectorySelection, bool, error) {
	info, err := os.Lstat(file)
	if errors.Is(err, os.ErrNotExist) {
		return DirectorySelection{}, false, nil
	}
	if err != nil {
		return DirectorySelection{}, false, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return DirectorySelection{}, false, errors.New("service pin must be a regular private file (mode 0600)")
	}
	// #nosec G304 -- fixed user configuration path, never a participant path.
	f, err := os.Open(file)
	if err != nil {
		return DirectorySelection{}, false, err
	}
	defer func() { _ = f.Close() }()
	const limit = 64 << 10
	body, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || len(body) > limit {
		return DirectorySelection{}, false, errors.New("service pin is unreadable or exceeds 64 KiB")
	}
	var cfg serviceDirectoryConfig
	meta, err := toml.Decode(string(body), &cfg)
	if err != nil {
		return DirectorySelection{}, false, err
	}
	if cfg.Version != 1 || !filepath.IsAbs(cfg.Dir) || len(meta.Undecoded()) != 0 {
		return DirectorySelection{}, false, errors.New("service pin requires version=1, an absolute data_dir " +
			"and no unknown fields")
	}
	if err := cfg.validatePath(); err != nil {
		return DirectorySelection{}, false, err
	}
	selected := DirectorySelection{Dir: cfg.Dir}
	if cfg.Path != nil {
		selected.ServicePath, selected.ServicePathSource, selected.ServicePathSet = *cfg.Path, cfg.PathSource, true
	}
	return selected, true, nil
}

func (c serviceDirectoryConfig) validatePath() error {
	if c.Path == nil && c.PathSource == "" {
		return nil
	}
	if c.Path == nil || (c.PathSource != "migrated" && c.PathSource != "configured") || strings.ContainsRune(*c.Path, 0) {
		return errors.New("service_path must have source migrated or configured and contain " +
			"no NUL; remove both path fields to use the default")
	}
	return nil
}

// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/agenxy/dibs/internal/paths"
	"github.com/agenxy/dibs/internal/plugins"
)

func stampCodexPlugin(files map[string]string, toolsHash string) (string, error) {
	return plugins.StampCodex(files, toolsHash)
}

// os.Executable resolves a package-manager symlink to its versioned target.
// Prefer an existing stable entry only when file identity proves it launches
// this exact image. Never create a launcher or choose a different PATH binary.
func pluginDibsCommand() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	image, err := os.Stat(exe)
	if err != nil {
		return "", err
	}
	candidates := []string{"dibs", os.Args[0]}
	for _, candidate := range candidates {
		path := candidate
		if !filepath.IsAbs(path) && !strings.ContainsAny(path, `/\`) {
			path, err = exec.LookPath(path)
			if err != nil {
				continue
			}
		}
		path, err = filepath.Abs(path)
		if err != nil {
			continue
		}
		// #nosec G703 -- metadata only; a caller-supplied entry is accepted solely by SameFile with this image
		info, statErr := os.Stat(path)
		if statErr == nil && os.SameFile(image, info) {
			// Canonicalise the parent only: resolving the final symlink would
			// reintroduce the versioned package path we deliberately avoided.
			return filepath.Join(paths.Canonical(filepath.Dir(path)), filepath.Base(path)), nil
		}
	}
	return filepath.Join(paths.Canonical(filepath.Dir(exe)), filepath.Base(exe)), nil
}

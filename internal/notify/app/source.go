// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

// Package app composes the native notifier's shipped Swift translation unit.
// Build tools and fixtures share this catalog; it is not linked into dibd.
package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ReadSource returns the main file followed by every .inc file in lexical
// order. Swift's top-level entry point requires one translation unit. dir is
// a maintainer-selected source directory, never participant-written input.
func ReadSource(dir string) ([]byte, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	names := []string{"notify_darwin.swift"}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".inc") {
			names = append(names, entry.Name())
		}
	}
	var source []byte
	for _, name := range names {
		// #nosec G304 -- fixed main name or directory entry under the operator's source directory.
		body, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("notifier source %s: %w", name, err)
		}
		source = append(source, body...)
		source = append(source, '\n')
	}
	return source, nil
}

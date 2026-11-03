// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package harnessenv

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// Keep every package test away from the person's real desktop lock while
// retaining the production lock resolution, acquisition and fallback decisions.
func TestMain(m *testing.M) {
	cache, err := os.MkdirTemp("", "dibs-background-cache-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	backgroundPairCacheDir = func() (string, error) { return cache, nil }
	// Claude session/settings and the default Codex home are also profile
	// reads. Individual fixtures may replace these with another private home.
	for key, value := range map[string]string{
		"HOME": cache, "USERPROFILE": cache, "XDG_CACHE_HOME": filepath.Join(cache, "cache"), "LOCALAPPDATA": filepath.Join(cache, "cache"),
	} {
		if err := os.Setenv(key, value); err != nil {
			panic(err)
		}
	}
	code := m.Run()
	_ = os.RemoveAll(cache)
	os.Exit(code)
}

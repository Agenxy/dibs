// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestScorerNativeCaseRootAndSuppressionKey(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("native case spelling is a Darwin contract")
	}
	root := filepath.Join(t.TempDir(), "Supgang")
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(filepath.Dir(root), "SupGang")
	a, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.Stat(alias)
	if os.IsNotExist(err) {
		t.Skip("fixture volume is case-sensitive")
	}
	if err != nil || !os.SameFile(a, b) {
		t.Fatalf("setup did not prove one root: %v", err)
	}
	if repositoryOf(alias) != repositoryOf(root) {
		t.Error("discovery indexed the same native root under two spellings")
	}
	if scorerAdviceKey("local", alias) != scorerAdviceKey("local", root) {
		t.Error("native root spelling reset scorer suppression")
	}
	if scorerAdviceKey("one", root) == scorerAdviceKey("two", root) {
		t.Fatal("suppression key lost the host")
	}
}

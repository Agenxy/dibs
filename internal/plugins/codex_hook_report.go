// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package plugins

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
)

// CodexHookReport is the bounded, read-only codex-hooks --json contract. It
// carries no hook inputs, user configuration, source paths or approval hashes.
type CodexHookReport struct {
	Binary string              `json:"binary"`
	Hooks  []CodexHookApproval `json:"hooks"`
}

// CodexHookApproval distinguishes approval, the owner's switch, and byte
// equality. None of these fields is an execution or delivery receipt.
type CodexHookApproval struct {
	Event          string `json:"event"`
	Trust          string `json:"trust"`
	Enabled        *bool  `json:"enabled,omitempty"`
	ShippedContent bool   `json:"shipped_content"`
}

// CodexHookSourceShipped compares actual local source bytes, not plugin names,
// path spelling or Codex's revision-dependent hash algorithm. This classifies
// a warning only: it is never permission to record a new hook approval.
func CodexHookSourceShipped(path string) bool {
	if !filepath.IsAbs(path) {
		return false
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	f, err := os.Open(path) // #nosec G304 G703 -- source path returned by the local Codex runtime; bounded, read-only.
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	body, err := io.ReadAll(io.LimitReader(f, (64<<10)+1))
	if err != nil || len(body) > 64<<10 {
		return false
	}
	want, err := files.ReadFile("data/codex/hooks.json")
	return err == nil && bytes.Equal(body, want)
}

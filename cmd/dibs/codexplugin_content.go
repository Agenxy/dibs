// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/agenxy/dibs/internal/paths"
)

// Hash all actual payload inputs and the served tool schema. Only our own
// generated version/stamp are excluded to avoid a self-referential digest.
// A Dibs release label or commit is not plugin content and must never enter
// these approved bytes: an unchanged plugin must not ask for approval again.
func stampCodexPlugin(files map[string]string, toolsHash string) (string, error) {
	var manifest map[string]any
	if err := json.Unmarshal([]byte(files[".codex-plugin/plugin.json"]), &manifest); err != nil {
		return "", err
	}
	delete(manifest, "version")
	canonical, err := json.Marshal(manifest)
	if err != nil {
		return "", err
	}
	files[".codex-plugin/plugin.json"] = string(canonical)
	delete(files, ".dibs-tools.json")
	raw, err := json.Marshal(struct {
		Files     map[string]string `json:"files"`
		ToolsHash string            `json:"tools_hash"`
	}{files, toolsHash})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])
	v := "content-tools-" + toolsHash + "-" + digest
	manifest["version"] = v
	b, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return "", err
	}
	files[".codex-plugin/plugin.json"] = string(b) + "\n"
	stamp, err := json.Marshal(map[string]string{"tools_hash": toolsHash, "bundle_hash": digest})
	if err != nil {
		return "", err
	}
	files[".dibs-tools.json"] = string(stamp) + "\n"
	return v, nil
}

// os.Executable resolves a package-manager symlink to its versioned target.
// Prefer an existing stable entry only when file identity proves it launches
// this exact image. Never create a launcher or choose a different PATH binary.
func codexPluginCommand() (string, error) {
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

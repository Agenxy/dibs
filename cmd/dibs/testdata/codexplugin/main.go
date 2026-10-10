// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

// A local Codex plugin-installer fixture. It accepts the production argv,
// copies the actual materialized plugin, and can fail before or after mutation.
package main

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func main() {
	if len(os.Args) != 9 || os.Args[1] != "-c" || os.Args[2] != `marketplaces.dibs.source_type="local"` ||
		os.Args[3] != "-c" || os.Args[5] != "plugin" || os.Args[6] != "add" || os.Args[7] != "dibs@dibs" || os.Args[8] != "--json" {
		os.Exit(3)
	}
	home := os.Getenv("CODEX_HOME")
	mark := filepath.Join(home, "installer-called")
	if err := os.WriteFile(mark, []byte(strings.Join(os.Args[1:], "\n")), 0o600); err != nil {
		panic(err)
	}
	mode := os.Getenv("DIBS_PLUGIN_FIXTURE")
	if mode == "fail" {
		os.Exit(4)
	}
	source, err := strconv.Unquote(strings.TrimPrefix(os.Args[4], "marketplaces.dibs.source="))
	if err != nil {
		panic(err)
	}
	plugin := filepath.Join(source, "plugin")
	b, err := os.ReadFile(filepath.Join(plugin, ".codex-plugin", "plugin.json"))
	if err != nil {
		panic(err)
	}
	var manifest struct{ Version string }
	if err := json.Unmarshal(b, &manifest); err != nil {
		panic(err)
	}
	base := filepath.Join(home, "plugins", "cache", "dibs", "dibs")
	if err := os.RemoveAll(base); err != nil {
		panic(err)
	}
	root := filepath.Join(base, manifest.Version)
	err = filepath.WalkDir(plugin, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(plugin, path)
		if err != nil {
			return err
		}
		dest := filepath.Join(root, rel)
		if entry.IsDir() {
			return os.MkdirAll(dest, 0o700)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(dest, b, 0o600)
	})
	if err != nil {
		panic(err)
	}
	if mode == "partial" {
		if err := os.WriteFile(filepath.Join(root, ".mcp.json"), []byte("{}"), 0o600); err != nil {
			panic(err)
		}
	}
	if mode == "late-fail" {
		os.Exit(5)
	}
	receiptVersion := manifest.Version
	if mode == "wrong-receipt" {
		receiptVersion = "wrong"
	}
	if err := json.NewEncoder(os.Stdout).Encode(map[string]string{
		"pluginId": "dibs@dibs", "version": receiptVersion, "installedPath": root,
	}); err != nil {
		panic(err)
	}
}

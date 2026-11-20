// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package plugins

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// CodexUpdate explains how to refresh the installed Dibs plugin.
const CodexUpdate = "Run `dibs codex-plugin install`."

// CodexCacheBase is the cache parent for the canonical Dibs plugin.
func CodexCacheBase(home string) string {
	return filepath.Join(home, "plugins", "cache", "dibs", "dibs")
}

// CodexPayload prepares the embedded plugin for a specific bridge command.
func CodexPayload(command, toolsHash string) (map[string]string, string, error) {
	plugin, ok := For("codex")
	if !ok {
		return nil, "", errors.New("embedded Codex plugin unavailable")
	}
	var servers struct {
		Servers map[string]map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(plugin.Files[".mcp.json"]), &servers); err != nil {
		return nil, "", err
	}
	servers.Servers["dibs"]["command"] = command
	b, err := json.MarshalIndent(servers, "", "  ")
	if err != nil {
		return nil, "", err
	}
	plugin.Files[".mcp.json"] = string(b) + "\n"
	version, err := StampCodex(plugin.Files, toolsHash)
	return plugin.Files, version, err
}

// CodexHome resolves the current user’s Codex configuration directory.
func CodexHome() (string, error) {
	if home := os.Getenv("CODEX_HOME"); home != "" {
		return filepath.Abs(home)
	}
	home, err := os.UserHomeDir()
	return filepath.Join(home, ".codex"), err
}

// CodexInstalled checks for an enabled canonical plugin and a cache directory.
func CodexInstalled(home string) (bool, error) {
	// #nosec G304 G703 -- operator's Codex config, read only
	b, err := os.ReadFile(filepath.Join(home, "config.toml"))
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var config struct {
		Plugins map[string]struct{ Enabled *bool }
	}
	if err := toml.Unmarshal(b, &config); err != nil {
		return false, err
	}
	entry, exists := config.Plugins["dibs@dibs"]
	if !exists || entry.Enabled != nil && !*entry.Enabled {
		return false, nil
	}
	entries, err := os.ReadDir(CodexCacheBase(home))
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			return true, nil
		}
	}
	return false, nil
}

// StampCodex hashes all actual payload inputs and the served tool schema. Only our own
// generated version/stamp are excluded to avoid a self-referential digest.
// A Dibs release label or commit is not plugin content and must never enter
// these approved bytes: an unchanged plugin must not ask for approval again.
func StampCodex(files map[string]string, toolsHash string) (string, error) {
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

// VerifyCodexFiles checks the installed regular files against the expected payload.
func VerifyCodexFiles(root string, files map[string]string) error {
	for name, want := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		info, err := os.Lstat(path) // #nosec G703 -- expected files below Codex's returned install root
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("plugin file is not regular: %s", path)
		}
		got, err := os.ReadFile(path) // #nosec G304 G703 -- same verified regular file
		if err != nil {
			return err
		}
		if !bytes.Equal(got, []byte(want)) {
			return fmt.Errorf("installed plugin bytes differ: %s", path)
		}
	}
	return nil
}

// CurrentCodexRoot verifies one active cache root, its receipt and the expected files.
func CurrentCodexRoot(home, version string, files map[string]string) bool {
	installed, err := CodexInstalled(home)
	if err != nil || !installed {
		return false
	}
	entries, err := os.ReadDir(CodexCacheBase(home))
	if err != nil || len(entries) != 1 || entries[0].Name() != version || !entries[0].IsDir() {
		return false
	}
	// #nosec G304 G703 -- own verified-install receipt
	b, err := os.ReadFile(filepath.Join(home, "dibs-marketplace", "receipt.json"))
	var receipt struct{ Version string }
	return err == nil && json.Unmarshal(b, &receipt) == nil && receipt.Version == version &&
		VerifyCodexFiles(filepath.Join(CodexCacheBase(home), version), files) == nil
}

// CodexRootStatus measures the installed content with the command from that
// root. A daemon may be inside an app bundle, so its own executable path tells
// it nothing about the CLI command in Codex. Command currency is checked
// separately by the bridge and PATH diagnostics.
func CodexRootStatus(home, toolsHash string) (installed, current bool, err error) {
	installed, err = CodexInstalled(home)
	if err != nil || !installed {
		return installed, false, err
	}
	root := CodexCacheBase(home)
	entries, err := os.ReadDir(root)
	if err != nil {
		return true, false, err
	}
	if len(entries) != 1 || !entries[0].IsDir() {
		return true, false, errors.New("ambiguous Codex plugin activation root")
	}
	// #nosec G304 G703 -- local installed plugin metadata, never executed
	body, err := os.ReadFile(filepath.Join(root, entries[0].Name(), ".mcp.json"))
	if err != nil {
		return true, false, err
	}
	var manifest struct {
		Servers map[string]struct{ Command string } `json:"mcpServers"`
	}
	if err := json.Unmarshal(body, &manifest); err != nil {
		return true, false, err
	}
	command := manifest.Servers["dibs"].Command
	if command == "" {
		return true, false, errors.New("missing Dibs command in Codex plugin")
	}
	files, version, err := CodexPayload(command, toolsHash)
	if err != nil || entries[0].Name() != version {
		return true, false, err
	}
	if !CurrentCodexRoot(home, version, files) {
		return true, false, errors.New("receipt or payload verification failed for the current Codex plugin root")
	}
	return true, true, nil
}

// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/agenxy/dibs/internal/mcp"
)

const (
	antigravityPluginName       = "dibs"
	antigravityManualDibsServer = "dibs"
	// Antigravity 2.34.0 prefixes plugin MCP server names with the sanitized
	// plugin name. Our manifest name already uses only lowercase ASCII.
	antigravityNativeDibsServer = antigravityPluginName + "_" + antigravityManualDibsServer
)

func antigravityConfigDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".gemini", "config"), nil
}

func trustAntigravityPlugin() error {
	dir, err := antigravityConfigDir()
	if err != nil {
		return err
	}
	servers := antigravityDibsServers(dir)
	if len(servers) == 0 {
		// Keep the manual setup's existing ability to grant before adding its server.
		servers = []string{antigravityManualDibsServer}
	}
	added, err := addAntigravityDibsGrants(filepath.Join(dir, "config.json"), servers)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("open Antigravity once to create its desktop profile, then run %s: %w",
				antigravityTrustCommand, err)
		}
		return fmt.Errorf("update Antigravity desktop permissions: %w", err)
	}
	if added != 0 {
		fmt.Printf("Added %d Antigravity Dibs MCP allow rule(s).\n", added)
	} else {
		fmt.Printf("Antigravity already has wildcard and per-tool allow rules for %s.\n", strings.Join(servers, ", "))
	}
	return nil
}

type antigravityGrantConfig struct {
	root, settings, grants map[string]json.RawMessage
	allow                  []string
}

func decodeAntigravityGrants(data []byte) (antigravityGrantConfig, error) {
	var config antigravityGrantConfig
	if err := json.Unmarshal(data, &config.root); err != nil || config.root == nil {
		return config, fmt.Errorf("expected a JSON object in Antigravity settings")
	}
	config.settings = map[string]json.RawMessage{}
	if raw := config.root["userSettings"]; raw != nil {
		if err := json.Unmarshal(raw, &config.settings); err != nil || config.settings == nil {
			return config, fmt.Errorf("expected a JSON object in Antigravity userSettings")
		}
	}
	config.grants = map[string]json.RawMessage{}
	if raw := config.settings["globalPermissionGrants"]; raw != nil {
		if err := json.Unmarshal(raw, &config.grants); err != nil || config.grants == nil {
			return config, fmt.Errorf("expected a JSON object in Antigravity globalPermissionGrants")
		}
	}
	if raw := config.grants["allow"]; raw != nil {
		if err := json.Unmarshal(raw, &config.allow); err != nil {
			return config, fmt.Errorf("expected an array of strings in Antigravity allow rules")
		}
	}
	return config, nil
}

// Count exact rules even when the documented wildcard is already present.
// Desktop matching of that form has yet to be measured independently.
func missingAntigravityToolGrants(allow []string, server string) []string {
	var missing []string
	for _, tool := range mcp.ToolNames() {
		rule := antigravityMCPGrant(server, tool)
		if !slices.Contains(allow, rule) {
			missing = append(missing, rule)
		}
	}
	return missing
}

func antigravityMCPGrant(server, tool string) string { return "mcp(" + server + "/" + tool + ")" }

func addAntigravityDibsGrants(path string, servers []string) (int, error) {
	settings, err := readAntigravitySettings(path)
	if err != nil {
		return 0, err
	}
	config, err := decodeAntigravityGrants(settings.data)
	if err != nil {
		return 0, err
	}
	before := len(config.allow)
	for _, server := range servers {
		wildcard := antigravityMCPGrant(server, "*")
		if !slices.Contains(config.allow, wildcard) {
			config.allow = append(config.allow, wildcard)
		}
		config.allow = append(config.allow, missingAntigravityToolGrants(config.allow, server)...)
	}
	added := len(config.allow) - before
	if added == 0 {
		return 0, nil
	}
	// These values are JSON validated on read and contain no custom encoders.
	config.grants["allow"], _ = json.Marshal(config.allow)
	config.settings["globalPermissionGrants"], _ = json.Marshal(config.grants)
	config.root["userSettings"], _ = json.Marshal(config.settings)
	updated, err := json.MarshalIndent(config.root, "", "  ")
	if err != nil {
		return 0, err
	}
	if err := settings.replace(append(updated, '\n')); err != nil {
		return 0, err
	}
	return added, nil
}

type antigravitySettingsFile struct {
	path string
	mode os.FileMode
	data []byte
}

func readAntigravitySettings(path string) (antigravitySettingsFile, error) {
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		return antigravitySettingsFile{}, err
	}
	info, err := os.Stat(target)
	if err != nil {
		return antigravitySettingsFile{}, err
	}
	if !info.Mode().IsRegular() {
		return antigravitySettingsFile{}, fmt.Errorf("expected a regular file for Antigravity settings")
	}
	// #nosec G304 -- derived from the operator's Antigravity desktop config path
	data, err := os.ReadFile(target)
	return antigravitySettingsFile{target, info.Mode().Perm(), data}, err
}

func (f antigravitySettingsFile) replace(updated []byte) error {
	return replaceAntigravitySettings(f.path, f.mode, f.data, updated)
}

func replaceAntigravitySettings(path string, mode os.FileMode, original, updated []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".dibs-antigravity-")
	if err != nil {
		return err
	}
	defer func() { _ = f.Close(); _ = os.Remove(f.Name()) }() // #nosec G703 -- own temporary settings file
	if _, err := f.Write(updated); err != nil {
		return err
	}
	if err := f.Chmod(mode); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	// #nosec G304 -- recheck the same resolved user settings file before replacement
	current, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if !bytes.Equal(current, original) {
		return fmt.Errorf("retry because Antigravity settings changed during this command")
	}
	// #nosec G703 -- replace the selected desktop settings atomically
	return os.Rename(f.Name(), path)
}

func (d *diagnosis) checkAntigravityGrants() {
	dir, err := antigravityConfigDir()
	if err != nil {
		return
	}
	servers := antigravityDibsServers(dir)
	if len(servers) == 2 {
		d.warn("Antigravity has Dibs MCP servers in both the profile and the native plugin", antigravityPluginUsage)
	}
	if len(servers) == 0 {
		return
	}
	// #nosec G304 -- read only the local Antigravity desktop settings
	data, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		d.warn("Antigravity desktop permissions could not be read", "open Antigravity's permission settings")
		return
	}
	config, err := decodeAntigravityGrants(data)
	if err != nil {
		d.warn("Antigravity desktop permissions could not be parsed", "open Antigravity's permission settings")
		return
	}
	for _, server := range servers {
		if missing := len(missingAntigravityToolGrants(config.allow, server)); missing > 0 {
			d.warn(fmt.Sprintf("%d Dibs MCP tool(s) on %s lack an explicit Antigravity allow rule", missing, server),
				antigravityTrustCommand)
		}
	}
}

func antigravityDibsServers(dir string) []string {
	var servers []string
	if hasAntigravityDibsServer(filepath.Join(dir, "mcp_config.json")) {
		servers = append(servers, antigravityManualDibsServer)
	}
	if hasAntigravityDibsServer(antigravityPluginServerPath(dir)) {
		servers = append(servers, antigravityNativeDibsServer)
	}
	return servers
}

func hasAntigravityDibsServer(path string) bool {
	server, err := readAntigravityDibsServer(path)
	return err == nil && server != nil
}

func readAntigravityDibsServer(path string) (json.RawMessage, error) {
	file, err := readAntigravitySettings(path)
	if err != nil {
		return nil, err
	}
	_, servers, err := decodeAntigravityServers(file.data)
	return servers["dibs"], err
}

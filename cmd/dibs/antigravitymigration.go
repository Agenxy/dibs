// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

func antigravityPluginServerPath(dir string) string {
	return filepath.Join(dir, "plugins", "dibs", "mcp_config.json")
}

func antigravityServerMigration(dir string) (antigravitySettingsFile, json.RawMessage, []byte, error) {
	path := filepath.Join(dir, "mcp_config.json")
	profile, err := readAntigravitySettings(path)
	if errors.Is(err, os.ErrNotExist) {
		if _, statErr := os.Lstat(path); errors.Is(statErr, os.ErrNotExist) {
			return profile, nil, nil, nil
		}
	}
	if err != nil {
		return profile, nil, nil, err
	}
	root, servers, err := decodeAntigravityServers(profile.data)
	if err != nil || servers["dibs"] == nil {
		return profile, nil, nil, err
	}
	manual := servers["dibs"]
	var entry map[string]json.RawMessage
	if err := json.Unmarshal(manual, &entry); err != nil || entry == nil {
		return profile, nil, nil, fmt.Errorf("expected an object for the existing Dibs MCP server")
	}
	delete(servers, "dibs")
	root["mcpServers"], _ = json.Marshal(servers) // validated raw JSON
	updated, err := json.MarshalIndent(root, "", "  ")
	return profile, manual, append(updated, '\n'), err
}

func decodeAntigravityServers(data []byte) (root, servers map[string]json.RawMessage, err error) {
	if err := json.Unmarshal(data, &root); err != nil || root == nil {
		return nil, nil, fmt.Errorf("expected a JSON object in Antigravity MCP configuration")
	}
	if raw := root["mcpServers"]; raw != nil {
		if err := json.Unmarshal(raw, &servers); err != nil || servers == nil {
			return nil, nil, fmt.Errorf("expected a JSON object in Antigravity mcpServers")
		}
	}
	return root, servers, nil
}

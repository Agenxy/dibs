// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package plugins

import (
	"encoding/json"
	"slices"
	"testing"
)

func TestGeminiPayloadIsAnInstallableExtension(t *testing.T) {
	plugin, ok := For("gemini-cli")
	if !ok {
		t.Fatal("Gemini CLI is missing from dibs://plugin")
	}
	var manifest struct {
		Name       string `json:"name"`
		Version    string `json:"version"`
		MCPServers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
			Trust   *bool    `json:"trust"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(plugin.Files["gemini-extension.json"]), &manifest); err != nil {
		t.Errorf("Gemini extension manifest: %v", err)
	}
	if server := manifest.MCPServers["dibs"]; manifest.Name != "dibs" || manifest.Version == "" ||
		server.Command != "dibs" || !slices.Equal(server.Args, []string{"mcp-stdio"}) || server.Trust != nil {
		t.Error("Gemini extension must declare the Dibs stdio server and leave approval to the operator")
	}
	var hooks struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal([]byte(plugin.Files["hooks/hooks.json"]), &hooks); err != nil {
		t.Errorf("Gemini extension lifecycle hooks: %v", err)
	}
	start := hooks.Hooks["SessionStart"]
	if len(hooks.Hooks) != 1 || len(start) != 1 || len(start[0].Hooks) != 1 ||
		start[0].Hooks[0].Type != "command" || start[0].Hooks[0].Command != "dibs hook-poll" {
		t.Error("Gemini extension must bind only the Dibs SessionStart hook")
	}
	if _, ok := plugin.Files["hooks.json"]; ok {
		t.Error("Gemini extension must put hooks under hooks/hooks.json")
	}
	if plugin.Files["skills/dibs/SKILL.md"] == "" {
		t.Error("Gemini extension is missing the coordination skill")
	}
}

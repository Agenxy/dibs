// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package plugins

import (
	"encoding/json"
	"slices"
	"testing"
)

func TestPortableDibsBundlesAreInstallable(t *testing.T) {
	for _, harness := range []string{"hermes", "openclaw"} {
		t.Run(harness, func(t *testing.T) {
			plugin, ok := For(harness)
			if !ok {
				t.Fatal("bundle is missing from dibs://plugin")
			}
			var manifest struct {
				Schema string `json:"$schema"`
				Name   string `json:"name"`
			}
			if err := json.Unmarshal([]byte(plugin.Files["plugin.json"]), &manifest); err != nil {
				t.Fatalf("portable manifest: %v", err)
			}
			if manifest.Schema != "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json" || manifest.Name != "dibs" {
				t.Error("bundle must identify itself as the Dibs Agent Plugins v1 package")
			}
			var mcp struct {
				Schema     string `json:"$schema"`
				MCPServers map[string]struct {
					Type    string            `json:"type"`
					Command string            `json:"command"`
					Args    []string          `json:"args"`
					Env     map[string]string `json:"env"`
				} `json:"mcpServers"`
			}
			if err := json.Unmarshal([]byte(plugin.Files["mcp.json"]), &mcp); err != nil {
				t.Fatalf("portable MCP config: %v", err)
			}
			server := mcp.MCPServers["dibs"]
			if mcp.Schema != "https://agent-plugins.org/schemas/1.0.0/mcp.schema.json" ||
				len(mcp.MCPServers) != 1 || server.Type != "stdio" || server.Command != "dibs" ||
				!slices.Equal(server.Args, []string{"mcp-stdio"}) || server.Env["DIBS_HARNESS"] != harness {
				t.Error("bundle must launch the Dibs stdio server with the harness fallback")
			}
			if plugin.Files["skills/dibs/SKILL.md"] == "" || plugin.Delivers {
				t.Error("bundle must ship the coordination skill and describe pull-only mail")
			}
		})
	}
}

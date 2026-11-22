// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestAntigravityPluginStagesTheSelectedBridge(t *testing.T) {
	root := t.TempDir()
	exe := filepath.Join(t.TempDir(), "dibs")
	env := map[string]string{"DIBS_ADDR": "127.0.0.1:49319", "DIBS_DIR": t.TempDir()}
	if err := stageAntigravityPlugin(root, exe, env); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"plugin.json", "rules/AGENTS.md", "skills/dibs/SKILL.md"} {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			t.Errorf("plugin is missing %s: %v", name, err)
		}
	}
	var config struct {
		MCPServers map[string]struct {
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env"`
		} `json:"mcpServers"`
	}
	file, err := os.Open(filepath.Join(root, "mcp_config.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	d := json.NewDecoder(file)
	d.DisallowUnknownFields()
	if err = d.Decode(&config); err != nil {
		t.Fatal(err)
	}
	server := config.MCPServers["dibs"]
	if len(config.MCPServers) != 1 || server.Command != exe ||
		!reflect.DeepEqual(server.Args, []string{"mcp-stdio"}) || !reflect.DeepEqual(server.Env, env) {
		t.Fatalf("incorrect staged MCP server: %+v", config.MCPServers)
	}
}

func TestAntigravityTrustPreservesProfile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	dir := filepath.Join(home, ".gemini", "config")
	pluginDir := filepath.Join(dir, "plugins", "dibs")
	if err := os.MkdirAll(pluginDir, 0o700); err != nil {
		t.Fatal(err)
	}
	settings := filepath.Join(dir, "config.json")
	original := []byte(`{"unrelated":{"value":1},"userSettings":{"theme":"system",` +
		`"globalPermissionGrants":{"allow":["mcp(other/tool)"],"deny":["mcp(dibs/other)"],"ask":["terminal(*)"]}}}`)
	if err := os.WriteFile(settings, original, 0o600); err != nil {
		t.Fatal(err)
	}
	d := &diagnosis{json: true}
	d.checkAntigravityGrants()
	if len(d.checks) != 0 {
		t.Fatalf("unconfigured profile produced checks: %+v", d.checks)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "mcp_config.json"), []byte(`{"mcpServers":{"dibs":{}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	d.checkAntigravityGrants()
	if len(d.checks) != 1 || d.checks[0].Level != "warning" || d.checks[0].Fix != antigravityTrustCommand {
		t.Fatalf("expected scoped grant guidance: %+v", d.checks)
	}
	if err := trustAntigravityPlugin(); err != nil {
		t.Fatal(err)
	}
	updated, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	expected := bytes.Replace(original, []byte(`["mcp(other/tool)"]`), []byte(`["mcp(other/tool)","mcp(dibs/*)"]`), 1)
	var got, want any
	if err := json.Unmarshal(expected, &want); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(updated, &got); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected Antigravity settings after granting trust: %s (%v)", updated, err)
	}
	d = &diagnosis{json: true}
	d.checkAntigravityGrants()
	if len(d.checks) != 0 {
		t.Fatalf("profile with scoped grant produced checks: %+v", d.checks)
	}
	if changed, err := addAntigravityDibsGrant(settings); err != nil || changed {
		t.Fatalf("repeated trust wrote the profile: changed=%v, err=%v", changed, err)
	}
	if err := replaceAntigravitySettings(settings, 0o600, original, original); err == nil {
		t.Fatal("concurrent Antigravity settings edit was overwritten")
	}
	if current, err := os.ReadFile(settings); err != nil || !bytes.Equal(current, updated) {
		t.Fatalf("repeat or concurrent update changed the profile: %s (%v)", current, err)
	}
}

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// checkDesktopShadowsPlugin catches the one Claude Desktop configuration that
// breaks Claude Code sessions instead of helping them.
//
// Claude Desktop hands the servers in its own claude_desktop_config.json to
// the Code tab as well, under their configured names, and a server named
// `dibs` there replaces the Claude Code plugin's `plugin:dibs:dibs` in every
// Code-tab session. The app's bridge is spawned from `/` without CLAUDE_PID,
// so an agent that registers through it binds `host-<pid>` rather than its
// session UUID, and its lifecycle hooks (which still run on the plugin's
// server) resolve to nobody: no guard, no mail, and nothing anywhere says why.
// Measured on 2026-09-15: the seat registered from a Code tab landed on
// harness `local-agent-mode-dibs` with cwd `/`.
//
// Either configuration alone is fine. Both together is the fault, and only a
// tool that reads both files can see it.
func checkDesktopShadowsPlugin(bad fixFn) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	desktop := filepath.Join(home, "Library", "Application Support", "Claude", "claude_desktop_config.json")
	if !desktopConfiguresDibs(desktop) || !claudeCodePluginInstalled(home) {
		return
	}
	bad("Claude Desktop's own config names a `dibs` server while the Claude Code plugin is installed",
		"the app hands that server to Code-tab sessions under the plugin's name and the plugin's "+
			"server disappears from them; agents registering there bind the app bridge's host-<pid> "+
			"and their hooks resolve to nobody. Remove the server in Claude Desktop's own "+
			"Settings > Developer, or QUIT the app first and then delete `mcpServers.dibs` from "+
			desktop+": the app keeps that table in memory and writes the whole file back on any "+
			"preference change, so an edit while it runs is undone. The plugin already covers "+
			"the Code tab, and the chat side gains nothing a hook could deliver")
}

// desktopConfiguresDibs reads the one field that matters from
// claude_desktop_config.json: whether `mcpServers` has a key named `dibs`.
func desktopConfiguresDibs(path string) bool {
	body, err := os.ReadFile(path) // #nosec G304 -- fixed well-known path
	if err != nil {
		return false
	}
	var cfg struct {
		Servers map[string]json.RawMessage `json:"mcpServers"`
	}
	if json.Unmarshal(body, &cfg) != nil {
		return false
	}
	_, has := cfg.Servers["dibs"]
	return has
}

// claudeCodePluginInstalled reads Claude Code's own record of installed
// plugins; the key is `<plugin>@<marketplace>` and ours is `dibs@dibs`.
func claudeCodePluginInstalled(home string) bool {
	record := filepath.Join(home, ".claude", "plugins", "installed_plugins.json")
	body, err := os.ReadFile(record) // #nosec G304 -- fixed well-known path
	if err != nil {
		return false
	}
	var rec struct {
		Plugins map[string]json.RawMessage `json:"plugins"`
	}
	if json.Unmarshal(body, &rec) != nil {
		return false
	}
	for key := range rec.Plugins {
		if strings.HasPrefix(key, "dibs@") {
			return true
		}
	}
	return false
}

// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/agenxy/dibs/internal/build"
	"github.com/agenxy/dibs/internal/clientdrift"
	"github.com/agenxy/dibs/internal/mcp"
	"github.com/agenxy/dibs/internal/plugins"
	"github.com/agenxy/dibs/internal/release"
)

const clientVersionHint = "Update if you are permitted to, otherwise tell your person, " +
	"and restart the session to load the update."

type behindClient struct {
	Kind    string `json:"kind"`
	Version string `json:"version"`
	Update  string `json:"update"`
}

type clientVersionNotice struct {
	DaemonBuild string         `json:"daemon_build"`
	Clients     []behindClient `json:"clients"`
	Hint        string         `json:"hint"`
}

func (n clientVersionNotice) text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Dibs client version is behind daemon %s.", n.DaemonBuild)
	for _, c := range n.Clients {
		fmt.Fprintf(&b, " %s %s. %s", strings.ReplaceAll(c.Kind, "_", " "), c.Version, c.Update)
	}
	b.WriteString(" " + clientVersionHint)
	return b.String()
}

func olderClient(kind, version, daemon, update string) (behindClient, bool) {
	if clientdrift.VersionLabel(version) != "" && clientdrift.VersionLabel(daemon) != "" {
		if order, err := release.Compare(version, daemon); err == nil && order < 0 {
			return behindClient{Kind: kind, Version: version, Update: update}, true
		}
	}
	return behindClient{}, false
}

func loadedPluginVersion(root, manifest string) string {
	if !filepath.IsAbs(root) {
		return ""
	}
	dir, err := os.OpenRoot(root)
	if err != nil {
		return ""
	}
	defer func() { _ = dir.Close() }()
	f, err := dir.Open(manifest)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	var p struct{ Name, Version string }
	if err := json.NewDecoder(io.LimitReader(f, 64<<10)).Decode(&p); err != nil || p.Name != "dibs" {
		return ""
	}
	return p.Version
}

func claudePluginVersion() string {
	if root := os.Getenv("CLAUDE_PLUGIN_ROOT"); root != "" {
		return loadedPluginVersion(root, ".claude-plugin/plugin.json")
	}
	home, _ := os.UserHomeDir()
	return plugins.ClaudeInstallVersion(home)
}

func bridgeVersionNotice(daemon string) clientVersionNotice {
	n := clientVersionNotice{DaemonBuild: daemon, Hint: clientVersionHint}
	if older, ok := olderClient("bridge_build", build.Version, daemon,
		"For a Homebrew install run `brew upgrade dibs`. If the new binary is already installed, restart the session."); ok {
		n.Clients = append(n.Clients, older)
	}
	if clientIs("claude-code") || clientIs("claude code") || os.Getenv("CLAUDE_PLUGIN_ROOT") != "" {
		if older, ok := olderClient("claude_plugin", claudePluginVersion(), daemon, plugins.ClaudeUpdate); ok {
			n.Clients = append(n.Clients, older)
		}
	}
	if root := os.Getenv("DIBS_CODEX_PLUGIN_ROOT"); root != "" {
		version := loadedPluginVersion(root, ".codex-plugin/plugin.json")
		if older, ok := olderClient("codex_plugin", version, daemon, plugins.CodexUpdate); ok {
			n.Clients = append(n.Clients, older)
		}
	}
	return n
}

// withClientVersionNotice advises at discovery, a catalog refresh and two
// successful activation replies. The backend provides version and first
// check-in evidence. The bridge reads its own installed versions and authors
// the advice, so an absent header cannot turn into a stale-client verdict.
func withClientVersionNotice(request, reply []byte, daemon string) []byte {
	if clientdrift.VersionLabel(daemon) == "" || !release.Comparable(daemon) {
		return reply
	}
	method, tool := methodAndTool(request)
	if !versionAdviceTarget(method, tool, request) {
		return reply
	}
	message := decodeVersionObject(reply)
	result, ok := message["result"].(map[string]any)
	if !ok || result["isError"] == true {
		return reply
	}
	meta, _ := result["_meta"].(map[string]any)
	if tool == "check_in" && meta[mcp.FirstCheckInMetaKey] != true {
		return reply
	}
	notice := bridgeVersionNotice(daemon)
	if len(notice.Clients) == 0 || !addVersionNotice(result, method, tool, notice) {
		return reply
	}
	out, err := json.Marshal(message)
	if err != nil {
		return reply
	}
	return out
}

func versionAdviceTarget(method, tool string, request []byte) bool {
	switch method {
	case "server/discover", "initialize", "tools/list":
		return true
	case "tools/call":
		return tool == "register" || tool == "check_in" || codexVersionHook(request, tool)
	default:
		return false
	}
}

func decodeVersionObject(raw []byte) map[string]any {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil
	}
	return m
}

func addVersionNotice(result map[string]any, method, tool string, notice clientVersionNotice) bool {
	switch method {
	case "server/discover", "initialize":
		instructions, _ := result["instructions"].(string)
		result["instructions"] = appendVersionContext(instructions, notice.text())
	case "tools/list":
		if !addVersionToolDescription(result, notice.text()) {
			return false
		}
	case "tools/call":
		return addVersionToolResult(result, tool, notice)
	default:
		return false
	}
	// A decorated discovery or catalog is local to this bridge.
	if _, cached := result["ttlMs"]; cached {
		result["ttlMs"], result["cacheScope"] = 0, "private"
	}
	return true
}

func addVersionToolDescription(result map[string]any, text string) bool {
	tools, _ := result["tools"].([]any)
	for _, item := range tools {
		tool, _ := item.(map[string]any)
		if tool["name"] == "register" {
			description, _ := tool["description"].(string)
			tool["description"] = appendVersionContext(description, text)
			return true
		}
	}
	return false
}

func addVersionToolResult(result map[string]any, tool string, notice clientVersionNotice) bool {
	content, _ := result["content"].([]any)
	if len(content) == 0 {
		return false
	}
	block, _ := content[0].(map[string]any)
	raw, _ := block["text"].(string)
	payload := decodeVersionObject([]byte(raw))
	if payload == nil || (tool == "register" && payload["token"] == nil) {
		return false
	}
	if tool == "hook_poll" {
		var plugin []behindClient
		for _, c := range notice.Clients {
			if c.Kind == "codex_plugin" {
				plugin = append(plugin, c)
			}
		}
		if len(plugin) == 0 {
			return false
		}
		notice.Clients = plugin
		addVersionHookContext(payload, notice)
	} else {
		payload["version_behind"] = notice
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return false
	}
	block["text"] = string(body)
	return true
}

func codexVersionHook(request []byte, tool string) bool {
	if tool != "hook_poll" || os.Getenv("DIBS_CODEX_PLUGIN_ROOT") == "" {
		return false
	}
	msg := decodeVersionObject(request)
	params, _ := msg["params"].(map[string]any)
	args, _ := params["arguments"].(map[string]any)
	return args["event"] == "SessionStart"
}

func appendVersionContext(existing, text string) string {
	if existing == "" {
		return text
	}
	return existing + "\n\n" + text
}

func addVersionHookContext(payload map[string]any, notice clientVersionNotice) {
	hook, _ := payload["hookSpecificOutput"].(map[string]any)
	if hook == nil {
		hook = map[string]any{"hookEventName": "SessionStart"}
		payload["hookSpecificOutput"] = hook
	}
	context, _ := hook["additionalContext"].(string)
	hook["additionalContext"] = appendVersionContext(context, notice.text())
}

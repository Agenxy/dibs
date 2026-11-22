// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/agenxy/dibs/internal/paths"
	"github.com/agenxy/dibs/internal/plugins"
	"github.com/agenxy/dibs/internal/procexec"
)

const (
	antigravityPluginUsage  = "dibs antigravity-plugin install"
	antigravityTrustCommand = "dibs antigravity-plugin --trust"
)

func antigravityPluginCmd(args []string) error {
	if helpOnly(args) {
		fmt.Println(antigravityPluginUsage)
		fmt.Println(antigravityTrustCommand + "  add a permission grant for the Dibs MCP server")
		return nil
	}
	if len(args) == 1 {
		switch args[0] {
		case "install":
			return adminOnly("antigravity-plugin", installAntigravityPlugin)
		case "--trust":
			return adminOnly("antigravity-plugin --trust", trustAntigravityPlugin)
		}
	}
	return fmt.Errorf("usage: %s or %s", antigravityPluginUsage, antigravityTrustCommand)
}

func installAntigravityPlugin() error {
	agy, err := exec.LookPath("agy")
	if err != nil {
		return fmt.Errorf("install the Antigravity CLI, then run %s: %w", antigravityPluginUsage, err)
	}
	exe, err := pluginDibsCommand()
	if err != nil {
		return err
	}
	scheme, _, err := resolveTransport(paths.DataDir())
	if err != nil {
		return err
	}
	root, err := os.MkdirTemp("", "dibs-antigravity-plugin-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(root) }() // #nosec G703 -- freshly created staging directory
	if err := stageAntigravityPlugin(root, exe, nonDefaultEnv(scheme)); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// #nosec G204 G702 -- operator's installed Antigravity CLI and a private staging directory
	cmd := procexec.Command(ctx, agy, "plugin", "install", root)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("antigravity plugin installer: %w", err)
	}
	return nil
}

func stageAntigravityPlugin(root, exe string, env map[string]string) error {
	p, ok := plugins.For("antigravity")
	if !ok {
		return fmt.Errorf("this Dibs binary has no embedded Antigravity plugin")
	}
	server := map[string]any{"command": exe, "args": []string{"mcp-stdio"}}
	if len(env) != 0 {
		server["env"] = env
	}
	config, err := json.MarshalIndent(map[string]any{"mcpServers": map[string]any{"dibs": server}}, "", "  ")
	if err != nil {
		return err
	}
	p.Files["mcp_config.json"] = string(config) + "\n"
	for name, body := range p.Files {
		if !fs.ValidPath(name) {
			return fmt.Errorf("invalid embedded plugin path %q", name)
		}
		if err := writePluginFile(filepath.Join(root, filepath.FromSlash(name)), []byte(body)); err != nil {
			return err
		}
	}
	return nil
}

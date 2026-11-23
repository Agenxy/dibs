// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"context"
	"encoding/json"
	"errors"
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
	antigravityPluginUsage    = "dibs antigravity-plugin install"
	antigravityTrustCommand   = "dibs antigravity-plugin --trust"
	antigravityPluginSynopsis = "usage: dibs antigravity-plugin <install|--trust>"
)

func antigravityPluginCmd(args []string) error {
	if helpOnly(args) {
		fmt.Println(antigravityPluginSynopsis)
		fmt.Println("  install  install the native Dibs plugin")
		fmt.Println("  --trust  add a permission grant for the Dibs MCP server")
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
	return errors.New(antigravityPluginSynopsis)
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
	dir, err := antigravityConfigDir()
	if err != nil {
		return err
	}
	return installAntigravityPayload(dir, root, exe, nonDefaultEnv(scheme), func() error {
		return runAntigravityInstaller(agy, root)
	})
}

func runAntigravityInstaller(agy, root string) error {
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

func installAntigravityPayload(dir, root, exe string, env map[string]string, install func() error) error {
	profile, manual, withoutDibs, err := antigravityServerMigration(dir)
	if err != nil {
		return fmt.Errorf("read Antigravity profile MCP configuration: %w", err)
	}
	pluginPath := antigravityPluginServerPath(dir)
	installed, err := readAntigravityDibsServer(pluginPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read existing Antigravity plugin MCP configuration: %w", err)
	}
	server := manual
	if server == nil {
		server = installed
	}
	if err := stageAntigravityPlugin(root, exe, env, server); err != nil {
		return err
	}
	if manual == nil {
		return install()
	}
	// Remove the manual entry before loading the plugin so a freshly migrated
	// profile never offers the same server through both configuration paths.
	if err := profile.replace(withoutDibs); err != nil {
		return fmt.Errorf("remove Dibs from Antigravity profile MCP configuration: %w", err)
	}
	if err := install(); err != nil {
		if installed == nil && hasAntigravityDibsServer(pluginPath) {
			return fmt.Errorf("inspect or retry the partially installed Antigravity Dibs plugin: %w", err)
		}
		restoreErr := replaceAntigravitySettings(profile.path, profile.mode, withoutDibs, profile.data)
		if restoreErr != nil {
			return errors.Join(err, fmt.Errorf("restore the original Dibs MCP entry: %w", restoreErr))
		}
		return err
	}
	fmt.Println("Moved the existing Dibs server from the Antigravity profile into the native plugin.")
	return nil
}

func stageAntigravityPlugin(root, exe string, env map[string]string, manual json.RawMessage) error {
	p, ok := plugins.For("antigravity")
	if !ok {
		return fmt.Errorf("this Dibs binary has no embedded Antigravity plugin")
	}
	var server any = manual
	if manual == nil {
		entry := map[string]any{"command": exe, "args": []string{"mcp-stdio"}}
		if len(env) != 0 {
			entry["env"] = env
		}
		server = entry
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

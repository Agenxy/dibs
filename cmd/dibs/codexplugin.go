// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/agenxy/dibs/internal/mcp"
	"github.com/agenxy/dibs/internal/plugins"
)

const codexPluginHelp = `dibs codex-plugin install [--dry-run] [--codex PATH]

Installs this binary's embedded Codex plugin through Codex's plugin installer.
Its build and tool fingerprint select a new version root, preserving dibs@dibs
and the configured marketplace source. No network fetch or app call is needed.
An explicit source for another root is preserved and refused: Codex would
replace the installed plugin from that source on its next sync. The error
prints how to remove that source if you choose this binary's plugin instead.

New chats and genuine app refreshes can load the changed definition. Running
chats keep their existing tool list until they end or the app refreshes it.
Hook trust is separate: check dibs codex-hooks after installation.

  --dry-run     print the version and installer command; change nothing
  --codex PATH  use this Codex executable instead of the discovered one
`

const codexPluginRepair = "dibs codex-plugin install"

// Executable discovery is an impure input; fixtures replace it before entering
// the real command/upgrade path so no test can reach the operator's installer.
var codexPluginExecutable = discoverCodexPluginExecutable

func discoverCodexPluginExecutable() string {
	// Measured installed app layout, 2026-10-10. The older hooks helper's
	// resource path no longer exists in this app; retain it as a fallback.
	path := "/Applications/ChatGPT.app/Contents/Resources/codex-cli/" +
		"CodexCLI.app/Contents/MacOS/codex"
	if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
		return path
	}
	return codexBinary()
}

func codexPluginCmd(args []string) error {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		fmt.Println("usage: dibs codex-plugin install [--dry-run] [--codex PATH]")
		fmt.Print(codexPluginHelp)
		return nil
	}
	if len(args) == 0 || args[0] != "install" {
		return fmt.Errorf("usage: %s", codexPluginHelp)
	}
	dry, bin := false, ""
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--dry-run", "-n":
			dry = true
		case "--codex":
			i++
			if i == len(args) || args[i] == "" {
				return errors.New("--codex needs an executable path")
			}
			bin = args[i]
		default:
			return fmt.Errorf("unknown codex-plugin argument %q; %s", args[i], codexPluginHelp)
		}
	}
	return adminOnly("codex-plugin", func() error {
		p, err := planCodexPlugin(bin)
		if err != nil {
			return err
		}
		if dry {
			return p.report(os.Stdout)
		}
		return p.install(os.Stdout)
	})
}

type codexPluginPlan struct {
	home, bin, version, source string
	files                      map[string]string
}

func codexPluginHome() (string, error) {
	if home := os.Getenv("CODEX_HOME"); home != "" {
		return filepath.Abs(home)
	}
	home, err := os.UserHomeDir()
	return filepath.Join(home, ".codex"), err
}

func planCodexPlugin(bin string) (*codexPluginPlan, error) {
	home, err := codexPluginHome()
	if err != nil {
		return nil, err
	}
	plugin, ok := plugins.For("codex")
	if !ok {
		return nil, errors.New("embedded Codex plugin unavailable")
	}
	exe, err := codexPluginCommand()
	if err != nil {
		return nil, err
	}
	var servers struct {
		Servers map[string]map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(plugin.Files[".mcp.json"]), &servers); err != nil {
		return nil, err
	}
	servers.Servers["dibs"]["command"] = exe
	b, err := json.MarshalIndent(servers, "", "  ")
	if err != nil {
		return nil, err
	}
	plugin.Files[".mcp.json"] = string(b) + "\n"
	v, err := stampCodexPlugin(plugin.Files, mcp.ToolsFingerprint())
	if err != nil {
		return nil, err
	}
	p := &codexPluginPlan{
		home: home, bin: bin, version: v,
		source: filepath.Join(home, "dibs-marketplace", v), files: plugin.Files,
	}
	if err := p.checkMarketplaceSource(); err != nil {
		return nil, err
	}
	if p.bin == "" {
		p.bin = codexPluginExecutable()
	}
	if p.bin == "" {
		return nil, fmt.Errorf("codex installer not found; install Codex then run %s", codexPluginRepair)
	}
	return p, nil
}

func (p *codexPluginPlan) args() []string {
	// Overrides affect this installer only. They neither replace the person's
	// marketplace source nor create a second Dibs plugin ID.
	source, _ := json.Marshal(p.source)
	return []string{
		"-c", `marketplaces.dibs.source_type="local"`, "-c", "marketplaces.dibs.source=" + string(source),
		"plugin", "add", "dibs@dibs", "--json",
	}
}

func (p *codexPluginPlan) report(out io.Writer) error {
	_, err := fmt.Fprintf(out, "would install %s\n%q %q\nRunning chats retain their existing tool list.\n",
		p.version, p.bin, p.args())
	return err
}

func (p *codexPluginPlan) cacheBase() string {
	return filepath.Join(p.home, "plugins", "cache", "dibs", "dibs")
}

// Only this installed and enabled identity is upgraded. A manual MCP setup,
// another marketplace's Dibs, or an intentionally disabled plugin is not consent
// to install/enable this integration.
func installedCodexPlugin(home string) (bool, error) {
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
	entries, err := os.ReadDir(filepath.Join(home, "plugins", "cache", "dibs", "dibs"))
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

func runCodexPlugin(bin string, args []string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// #nosec G204 G702 -- discovered operator executable or explicit --codex path; argv, never a shell
	cmd := exec.CommandContext(ctx, bin, args...)
	return cmd.CombinedOutput()
}

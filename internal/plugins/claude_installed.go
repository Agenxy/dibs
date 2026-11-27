// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package plugins

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/agenxy/dibs/internal/release"
)

// ClaudeUpdate is the operator command sequence for the canonical plugin.
const ClaudeUpdate = "Run `claude plugin marketplace update dibs`, then `claude plugin update dibs@dibs`."

type claudeInstall struct {
	Version string `json:"version"`
}

func claudeInstalls(home string) map[string][]claudeInstall {
	if home == "" {
		return nil
	}
	path := filepath.Join(home, ".claude", "plugins", "installed_plugins.json")
	body, err := os.ReadFile(path) // #nosec G304 G703 -- fixed local Claude Code record
	if err != nil {
		return nil
	}
	var record struct {
		Plugins map[string]json.RawMessage `json:"plugins"`
	}
	if json.Unmarshal(body, &record) != nil {
		return nil
	}
	installs := make(map[string][]claudeInstall)
	for key, raw := range record.Plugins {
		if strings.HasPrefix(key, "dibs@") {
			var entries []claudeInstall
			if json.Unmarshal(raw, &entries) == nil {
				installs[key] = entries
			}
		}
	}
	return installs
}

// ClaudeInstalled includes Dibs-named plugins from other marketplaces for
// doctor’s desktop shadowing check.
func ClaudeInstalled(home string) bool { return len(claudeInstalls(home)) > 0 }

// ClaudeInstallVersion identifies a single canonical installed version when
// the harness did not provide the loaded plugin root. Multiple versions are
// ambiguous, so they cannot identify this session's plugin.
func ClaudeInstallVersion(home string) string {
	var version string
	for _, entry := range claudeInstalls(home)["dibs@dibs"] {
		if entry.Version == "" || (version != "" && entry.Version != version) {
			return ""
		}
		version = entry.Version
	}
	return version
}

// ClaudeShippedVersion is the manifest release stamp, independent of the
// binary's development version. All plugin currency reports share it.
func ClaudeShippedVersion() string {
	data, err := files.ReadFile("data/claude-code/.claude-plugin/plugin.json")
	if err != nil {
		return ""
	}
	var manifest struct{ Version string }
	if json.Unmarshal(data, &manifest) != nil || !release.Comparable(manifest.Version) {
		return ""
	}
	return manifest.Version
}

// OlderClaudeVersions compares the canonical marketplace install records
// against the shipped manifest, including distinct installs in other scopes.
func OlderClaudeVersions(home string) (older []string, shipped string) {
	shipped = ClaudeShippedVersion()
	for _, entry := range claudeInstalls(home)["dibs@dibs"] {
		value := entry.Version
		if order, err := release.Compare(value, shipped); err == nil && order < 0 &&
			len(value) <= 128 && !strings.ContainsAny(value, "\r\n\t\x1b") {
			older = append(older, value)
		}
	}
	slices.Sort(older)
	return slices.Compact(older), shipped
}

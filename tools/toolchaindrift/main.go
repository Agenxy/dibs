// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

// Command toolchaindrift fails when a pinned tool has a newer upstream release.
//
// Dependabot keeps Go modules, Actions and bun packages current, but nothing
// watched mise.toml or the mise version the workflows install: Go sat a minor
// release behind, and golangci-lint 2.15 then found issues on a main that 2.12
// called clean. A gate run on a stale tool is a measurement of the past. It
// runs on a schedule, not in the gate, because an upstream release is not a
// defect in any one pull request.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
)

func main() {
	outdated, err := miseOutdated()
	if err != nil {
		fmt.Fprintln(os.Stderr, "toolchaindrift:", err)
		os.Exit(2)
	}
	pinned, err := workflowMiseVersions(filepath.Join(".github", "workflows"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "toolchaindrift:", err)
		os.Exit(2)
	}
	latest, err := latestMise()
	if err != nil {
		fmt.Fprintln(os.Stderr, "toolchaindrift:", err)
		os.Exit(2)
	}
	lines, behind := verdict(outdated, pinned, latest)
	for _, line := range lines {
		fmt.Println(line)
	}
	if behind {
		os.Exit(1)
	}
}

// outdatedTool is one entry of `mise outdated --bump --json`.
type outdatedTool struct {
	Current string `json:"current"`
	Latest  string `json:"latest"`
}

// verdict decides; the functions below only observe.
func verdict(outdated map[string]outdatedTool, pinned []string, latestMise string) ([]string, bool) {
	var lines []string
	names := make([]string, 0, len(outdated))
	for name := range outdated {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		t := outdated[name]
		lines = append(lines, fmt.Sprintf("behind: %s %s, upstream %s; bump mise.toml", name, t.Current, t.Latest))
	}
	for _, v := range pinned {
		if v != latestMise {
			lines = append(lines, fmt.Sprintf(
				"behind: workflows install mise %s, upstream %s; bump every `version:`", v, latestMise))
		}
	}
	if len(lines) == 0 {
		current := "current: every mise.toml pin and the workflows' mise " + latestMise + " are upstream's latest"
		return []string{current}, false
	}
	return lines, true
}

func miseOutdated() (map[string]outdatedTool, error) {
	out, err := exec.Command("mise", "outdated", "--bump", "--json").Output()
	if err != nil {
		return nil, fmt.Errorf("mise outdated: %w", err)
	}
	tools := map[string]outdatedTool{}
	if err := json.Unmarshal(out, &tools); err != nil {
		return nil, fmt.Errorf("reading mise outdated output: %w", err)
	}
	return tools, nil
}

var miseVersionLine = regexp.MustCompile(`(?m)^\s+version:\s*(20\d\d\.\d+\.\d+)\s*$`)

// workflowMiseVersions returns each distinct mise version the workflows pin.
func workflowMiseVersions(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var versions []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yml") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name())) // #nosec G304 -- this repo's own workflows
		if err != nil {
			return nil, err
		}
		for _, m := range miseVersionLine.FindAllStringSubmatch(string(raw), -1) {
			if !slices.Contains(versions, m[1]) {
				versions = append(versions, m[1])
			}
		}
	}
	if len(versions) == 0 {
		return nil, errors.New("no workflow pins a mise version; the pattern no longer matches mise-action's input")
	}
	return versions, nil
}

func latestMise() (string, error) {
	req, err := http.NewRequest(http.MethodGet, "https://api.github.com/repos/jdx/mise/releases/latest", nil)
	if err != nil {
		return "", err
	}
	if token := os.Getenv("GH_TOKEN"); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("asking GitHub for mise's latest release: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub answered %s for mise's latest release", resp.Status)
	}
	var release struct {
		Tag string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return "", fmt.Errorf("reading mise's latest release: %w", err)
	}
	return strings.TrimPrefix(release.Tag, "v"), nil
}

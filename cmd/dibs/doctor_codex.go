package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// checkCodexHookTrust: Codex has the Dibs hooks, and whether it will run them.
//
// Since Codex 0.153 a hook from the user's own configuration is UNTRUSTED
// until the person reviews it (the TUI's startup review or `/hooks`), and an
// untrusted hook is dropped at discovery without a word to the session. The
// whole delivery path then reads as installed and does nothing: the plugin is
// enabled, hooks.json is where the README says, and not one `hook_poll`
// arrives. Measured 2026-09-19 on 0.155.0-alpha.9.2: zero deliveries with
// the hooks in place, two per session with `--dangerously-bypass-hook-trust`,
// which is the flag that proves the gate is trust and nothing else.
//
// Trust is recorded in config.toml as `[hooks.state."<key>"] trusted_hash`.
// The hash is Codex's canonical fingerprint of the handler and the key is
// the source path plus event and index, so this does not try to compute
// either: it looks for the record and says how to make one when it is missing.
// The ChatGPT app writes it for a plugin it installs; the CLI does not.
func checkCodexHookTrust(ok reportFn, warn fixFn) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	config, err := os.ReadFile(filepath.Join(home, ".codex", "config.toml")) // #nosec G304 -- fixed well-known path
	if err != nil {
		return // no Codex here
	}
	body := string(config)
	plugin := codexPluginEnabled(body)
	hooksFile := filepath.Join(home, ".codex", "hooks.json")
	loose := fileNames(hooksFile, "hook_poll")
	if !plugin && !loose {
		return // Codex without the Dibs hooks: nothing to trust, nothing to say
	}
	if codexHooksTrusted(body) {
		ok("Codex has trusted the Dibs hooks (mail is delivered at its lifecycle boundaries)")
		return
	}
	where := "the Dibs plugin"
	if !plugin {
		where = hooksFile
	}
	warn("Codex has the Dibs hooks ("+where+") and has not trusted them, so they never run",
		"Codex drops a hook it has not reviewed and says nothing, so Codex agents get no "+
			"mail at their turn boundaries however well the plugin is installed. Start `codex` "+
			"in a terminal, run `/hooks`, and trust the Dibs entries (SessionStart, Stop, "+
			"SubagentStop); the ChatGPT app records that trust itself when the plugin is "+
			"installed from its own plugin list. Until then `dibs doctor` keeps saying this")
}

var (
	codexPluginPattern = regexp.MustCompile(`(?m)^\[plugins\."dibs@[^"]+"\]\s*$`)
	// A Dibs hook's key names either the plugin's cache path (which carries
	// the plugin name) or the root hooks.json the README ships for Codex.
	codexTrustPattern = regexp.MustCompile(`(?m)^\[hooks\.state\."[^"\n]*(?:dibs|\.codex/hooks\.json:)[^"\n]*"\]\s*$`)
)

// codexPluginEnabled reports a `[plugins."dibs@<marketplace>"]` table that is
// not explicitly disabled.
func codexPluginEnabled(config string) bool {
	loc := codexPluginPattern.FindStringIndex(config)
	if loc == nil {
		return false
	}
	rest := config[loc[1]:]
	if next := strings.Index(rest, "\n["); next >= 0 {
		rest = rest[:next]
	}
	return !strings.Contains(rest, "enabled = false")
}

// codexHooksTrusted reports at least one `[hooks.state."…"]` table for a Dibs
// hook that carries a trusted_hash. It cannot tell whether that hash still
// matches the file, which Codex reports as "modified" and treats as untrusted
// again; a stale record here reads as trusted, and the fix is the same.
func codexHooksTrusted(config string) bool {
	for _, loc := range codexTrustPattern.FindAllStringIndex(config, -1) {
		rest := config[loc[1]:]
		if next := strings.Index(rest, "\n["); next >= 0 {
			rest = rest[:next]
		}
		if strings.Contains(rest, "trusted_hash") {
			return true
		}
	}
	return false
}

func fileNames(path, needle string) bool {
	body, err := os.ReadFile(path) // #nosec G304 -- fixed well-known path
	return err == nil && strings.Contains(string(body), needle)
}

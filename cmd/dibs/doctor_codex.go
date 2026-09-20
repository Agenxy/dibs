package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
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
	where := "the Dibs plugin"
	if !plugin {
		where = hooksFile
	}
	// ASK CODEX, which is the only party that knows. The config scan below
	// finds a trust table and cannot tell whether its hash still matches the
	// hook (Codex reports that as "modified" and drops the hook again after a
	// plugin upgrade) or whether every hook has one; a stale record read as
	// "mail is delivered". Found by the pre-release review. The scan stays
	// as the fallback for a Codex whose app-server will not start.
	if hooks, err := askCodexHooks(); err == nil {
		reportCodexHooks(hooks, where, ok, warn)
		return
	}
	if codexHooksTrusted(body) {
		ok("Codex has a trust record for the Dibs hooks (its app-server would not answer, " +
			"so whether the record matches the current hooks is unverified; `dibs codex-hooks` asks it)")
		return
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

// askCodexHooks lists the Dibs hooks through Codex's app-server, the same
// way `dibs codex-hooks` does, bounded so doctor stays quick.
func askCodexHooks() ([]codexHook, error) {
	bin := codexBinary()
	if bin == "" {
		return nil, errors.New("no codex")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	app, err := startAppServer(ctx, bin)
	if err != nil {
		return nil, err
	}
	defer app.close()
	return app.dibsHooks()
}

// reportCodexHooks turns Codex's own view of the hooks into one doctor line.
func reportCodexHooks(hooks []codexHook, where string, ok reportFn, warn fixFn) {
	if len(hooks) == 0 {
		warn("Codex reports no Dibs hooks, though "+where+" is installed",
			"Codex did not discover the hooks: check `codex plugin list` shows dibs@dibs enabled, "+
				"or that ~/.codex/hooks.json parses; `dibs codex-hooks` shows what Codex sees")
		return
	}
	var untrusted []string
	for _, h := range hooks {
		if h.TrustStatus != "trusted" {
			untrusted = append(untrusted, h.EventName+" ("+h.TrustStatus+")")
		}
	}
	if len(untrusted) == 0 {
		ok(fmt.Sprintf("Codex has trusted all %d Dibs hooks (mail is delivered at its lifecycle boundaries)",
			len(hooks)))
		return
	}
	warn(fmt.Sprintf("Codex will not run %d of %d Dibs hooks: %s",
		len(untrusted), len(hooks), strings.Join(untrusted, ", ")),
		"an untrusted or modified hook is dropped at discovery without a word, so those "+
			"deliveries never happen. Run `dibs codex-hooks --trust`, which records trust the way "+
			"Codex's own /hooks does, or trust them there")
}

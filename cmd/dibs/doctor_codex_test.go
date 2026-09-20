package main

import (
	"strings"
	"testing"
)

// Trust is a table in config.toml, and only its presence with a hash counts.
//
// Codex drops an untrusted hook silently, so an installed plugin with no
// trust record is the shape that reads as working and delivers nothing.
// These readers decide whether doctor says so, and each must read exactly
// the field it claims: a plugin table that is disabled is not an installed
// plugin, and a hooks.state table without trusted_hash is not trust.
func TestCodexTrustIsReadFromTheConfigTablesOnly(t *testing.T) {
	if codexPluginEnabled("[plugins.\"gmail@openai-curated\"]\nenabled = true\n") {
		t.Error("another plugin is not ours")
	}
	if !codexPluginEnabled("[plugins.\"dibs@dibs\"]\nenabled = true\n") {
		t.Error("an enabled dibs plugin not seen")
	}
	if codexPluginEnabled("[plugins.\"dibs@dibs\"]\nenabled = false\n\n[other]\nx = 1\n") {
		t.Error("a disabled dibs plugin counted as installed")
	}
	untrusted := "[hooks.state.\"/Users/x/.codex/plugins/cache/dibs/dibs/0.0.7/hooks.json:stop:0:0\"]\nenabled = true\n"
	if codexHooksTrusted(untrusted) {
		t.Error("a state table without trusted_hash is not trust")
	}
	trusted := untrusted + "trusted_hash = \"sha256:abc\"\n"
	if !codexHooksTrusted(trusted) {
		t.Error("a trusted_hash on a Dibs hook not seen")
	}
	foreign := "[hooks.state.\"/Users/x/.codex/plugins/cache/other/x/1/hooks/hooks.json:stop:0:0\"]\ntrusted_hash = \"sha256:abc\"\n"
	if codexHooksTrusted(foreign) {
		t.Error("trust on another plugin's hooks is not ours (path has no dibs)")
	}
}

// Doctor reports what Codex says about each hook, not what a config table
// says about some hook: a stale trust record (hash no longer matching, which
// Codex reports as "modified") and a missing one both mean the hook is
// dropped, and both must read as not delivered. Found by the pre-release
// review.
func TestDoctorReportsCodexHooksAsCodexSeesThem(t *testing.T) {
	var oks, warns []string
	ok := func(m string) { oks = append(oks, m) }
	warn := func(w, _ string) { warns = append(warns, w) }

	reportCodexHooks(nil, "the Dibs plugin", ok, warn)
	if len(warns) != 1 || !strings.Contains(warns[0], "reports no Dibs hooks") {
		t.Errorf("no hooks discovered must warn: %q %q", oks, warns)
	}
	oks, warns = nil, nil
	reportCodexHooks([]codexHook{
		{EventName: "sessionStart", TrustStatus: "trusted"},
		{EventName: "stop", TrustStatus: "modified"},
		{EventName: "subagentStop", TrustStatus: "untrusted"},
	}, "the Dibs plugin", ok, warn)
	if len(warns) != 1 || !strings.Contains(warns[0], "2 of 3") || !strings.Contains(warns[0], "stop (modified)") {
		t.Errorf("a modified hook is a dropped hook and must be named: %q %q", oks, warns)
	}
	oks, warns = nil, nil
	reportCodexHooks([]codexHook{
		{EventName: "sessionStart", TrustStatus: "trusted"},
		{EventName: "stop", TrustStatus: "trusted"},
	}, "the Dibs plugin", ok, warn)
	if len(oks) != 1 || len(warns) != 0 || !strings.Contains(oks[0], "all 2") {
		t.Errorf("all trusted must be one ok line: %q %q", oks, warns)
	}
}

package main

import "testing"

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

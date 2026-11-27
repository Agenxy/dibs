// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/build"
	"github.com/agenxy/dibs/internal/plugins"
	"github.com/agenxy/dibs/internal/procexec"
)

const testVersionDaemonBuild = "0.0.19"

func fixtureVersionDaemon(t *testing.T) {
	t.Helper()
	old := build.Version
	build.Version = testVersionDaemonBuild
	t.Cleanup(func() { build.Version = old })
}

func fixtureClaudeInstall(t *testing.T, home, version string) {
	t.Helper()
	path := filepath.Join(home, ".claude", "plugins", "installed_plugins.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	record := `{"version":2,"plugins":{"dibs@dibs":[{"scope":"user","version":"` + version + `"}]}}`
	if err := os.WriteFile(path, []byte(record), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRealBridgeWarnsAboutOlderClientOnRegisterAndFirstCheckIn(t *testing.T) {
	fixtureVersionDaemon(t)
	for _, tc := range []struct {
		name, bridge, plugin string
		behind, oldDaemon    bool
	}{
		{"behind", "0.0.5", "0.0.9", true, false},
		{"current", testVersionDaemonBuild, testVersionDaemonBuild, false, false},
		{"old daemon has no build header", "0.0.5", "0.0.9", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRecoveryStdioWithBuild(t, "", tc.bridge)
			f.thread = ""
			f.stripBuild.Store(tc.oldDaemon)
			fixtureClaudeInstall(t, f.dir, tc.plugin)
			hello := f.exchange("server/discover", map[string]any{
				"clientInfo": map[string]any{"name": "claude-code"},
			})
			var handshake struct {
				Result struct{ Instructions string } `json:"result"`
			}
			if err := json.Unmarshal(hello, &handshake); err != nil || handshake.Result.Instructions == "" {
				t.Fatalf("setup: discovery instructions unparseable: %v", err)
			}
			if got := strings.Contains(handshake.Result.Instructions, "Dibs client version"); got != tc.behind {
				t.Errorf("discovery advice=%t, want %t", got, tc.behind)
			}
			list := f.exchange("tools/list", map[string]any{})
			if got := strings.Contains(string(list), "Dibs client version"); got != tc.behind {
				t.Errorf("tool catalog advice=%t, want %t", got, tc.behind)
			}
			register := recoveryStdioOK(t, f.call("register", map[string]any{
				"name": "drift-worker", "cwd": f.dir, "nonce": "drift-test-worker",
			}))
			checkIn := func() map[string]any {
				t.Helper()
				return recoveryStdioOK(t, f.call("check_in", map[string]any{"token": register["token"]}))
			}
			first, next := checkIn(), checkIn()
			for name, result := range map[string]map[string]any{"register": register, "first check_in": first} {
				notice, ok := result["version_behind"].(map[string]any)
				if ok != tc.behind {
					t.Errorf("%s advice=%t, want %t", name, ok, tc.behind)
					continue
				}
				if !ok {
					continue
				}
				body, _ := json.Marshal(notice)
				for _, evidence := range []string{tc.bridge, tc.plugin, testVersionDaemonBuild, "claude plugin update dibs@dibs", "if you are permitted"} {
					if !strings.Contains(string(body), evidence) {
						t.Errorf("%s advice omits %q", name, evidence)
					}
				}
			}
			if _, ok := next["version_behind"]; ok {
				t.Fatal("second check_in repeated the client warning")
			}
			// The same nonce in a different session is a genuine activation,
			// unlike a liveness wake on an unchanged credential.
			f.thread = "439f3469-4e55-47ab-b6b9-03095111e471"
			moved := recoveryStdioOK(t, f.call("register", map[string]any{
				"name": "drift-worker", "cwd": f.dir, "nonce": "drift-test-worker", "session_id": f.thread,
			}))
			if moved["agent_id"] != register["agent_id"] || moved["token"] == register["token"] {
				t.Fatal("setup: registering the new session did not move the identity")
			}
			register = moved
			_, warned := checkIn()["version_behind"]
			if warned != tc.behind {
				t.Errorf("first check_in of next activation advice=%t, want %t", warned, tc.behind)
			}
		})
	}
}

func TestCodexSessionStartUsesTheLoadedPluginVersion(t *testing.T) {
	fixtureVersionDaemon(t)
	root := t.TempDir()
	t.Setenv("DIBS_CODEX_PLUGIN_ROOT", root)
	f := newRecoveryStdioWithBuild(t, "", testVersionDaemonBuild)
	path := filepath.Join(root, ".codex-plugin", "plugin.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{"0.0.9", testVersionDaemonBuild, "content-tools-unordered"} {
		t.Run(version, func(t *testing.T) {
			record := `{"name":"dibs","version":"` + version + `"}`
			if err := os.WriteFile(path, []byte(record), 0o600); err != nil {
				t.Fatal(err)
			}
			payload := f.call("hook_poll", map[string]any{
				"session_id": f.thread, "event": "SessionStart", "cwd": root, "strict_output": true,
			})
			if payload["code"] != nil {
				t.Fatalf("setup: hook RPC was refused: %v", payload["code"])
			}
			body, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			want := version == "0.0.9"
			if got := strings.Contains(string(body), "dibs codex-plugin install"); got != want {
				t.Fatalf("Codex SessionStart advice=%t, want %t", got, want)
			}
		})
	}
}

func TestClientDriftHookProcess(t *testing.T) {
	if os.Getenv("DIBS_TEST_VERSION_HOOK") != "1" {
		return
	}
	if err := hookPoll([]string{"--claude-plugin"}); err != nil {
		t.Fatal(err)
	}
}

func TestClaudeSessionStartAddsVersionContextBeforeRegister(t *testing.T) {
	fixtureVersionDaemon(t)
	f := newRecoveryStdioWithBuild(t, "", testVersionDaemonBuild)
	for _, version := range []string{"0.0.9", testVersionDaemonBuild} {
		t.Run(version, func(t *testing.T) {
			fixtureClaudeInstall(t, f.dir, version)
			cmd := procexec.Command(t.Context(), os.Args[0], "-test.run=^TestClientDriftHookProcess$")
			cmd.Env = []string{
				"DIBS_TEST_VERSION_HOOK=1", "HOME=" + f.dir, "USERPROFILE=" + f.dir,
				"DIBS_DIR=" + f.dir, "DIBS_ADDR=" + strings.TrimPrefix(f.srv.URL, "http://"),
				"DIBS_HOST_ID=recovery-host", "SYSTEMROOT=" + os.Getenv("SYSTEMROOT"),
				"TMPDIR=" + os.TempDir(), "TEMP=" + os.TempDir(),
			}
			cmd.Stdin = strings.NewReader(`{"session_id":"client-drift-start","hook_event_name":"SessionStart"}`)
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("setup: SessionStart hook did not run: %v: %s", err, out)
			}
			var result struct {
				Hook struct {
					Event   string `json:"hookEventName"`
					Context string `json:"additionalContext"`
				} `json:"hookSpecificOutput"`
			}
			if err := json.NewDecoder(strings.NewReader(string(out))).Decode(&result); err != nil {
				t.Fatalf("setup: SessionStart hook output was not JSON: %v", err)
			}
			got := strings.Contains(result.Hook.Context, "claude plugin update dibs@dibs")
			if want := version != testVersionDaemonBuild; got != want || (want && result.Hook.Event != "SessionStart") {
				t.Fatalf("SessionStart drift context=%q, want advice=%t", result.Hook.Context, want)
			}
		})
	}
}

// A source daemon advances before release stamps its plugin manifests. An
// update that installs precisely those shipped bytes cannot clear a warning
// based on the daemon's unrelated binary version. Enter through both actual
// bridge discovery and the executable SessionStart hook, not a comparator.
func TestShippedClaudePluginIsCurrentWithAheadOfReleaseDaemon(t *testing.T) {
	plugin, ok := plugins.For("claude-code")
	if !ok {
		t.Fatal("missing shipped Claude plugin")
	}
	var manifest struct{ Version string }
	if err := json.Unmarshal([]byte(plugin.Files[".claude-plugin/plugin.json"]), &manifest); err != nil || manifest.Version == "" {
		t.Fatal("invalid shipped manifest", err)
	}
	old := build.Version
	build.Version = "99.0.0-dev"
	t.Cleanup(func() { build.Version = old })
	for _, tc := range []struct {
		name, version string
		warn          bool
	}{
		{"shipped", manifest.Version, false},
		{"older", "0.0.1", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRecoveryStdioWithBuild(t, "", build.Version)
			fixtureClaudeInstall(t, f.dir, tc.version)
			hello := f.exchange("server/discover", map[string]any{"clientInfo": map[string]any{"name": "claude-code"}})
			if got := strings.Contains(string(hello), "Dibs client version is behind"); got != tc.warn {
				t.Errorf("real bridge: plugin warning=%v, want %v: %s", got, tc.warn, hello)
			}
			cmd := procexec.Command(t.Context(), os.Args[0], "-test.run=^TestClientDriftHookProcess$")
			cmd.Env = []string{
				"DIBS_TEST_VERSION_HOOK=1", "HOME=" + f.dir, "USERPROFILE=" + f.dir,
				"DIBS_DIR=" + f.dir, "DIBS_ADDR=" + strings.TrimPrefix(f.srv.URL, "http://"),
				"DIBS_HOST_ID=recovery-host", "SYSTEMROOT=" + os.Getenv("SYSTEMROOT"),
				"TMPDIR=" + os.TempDir(), "TEMP=" + os.TempDir(),
			}
			cmd.Stdin = strings.NewReader(`{"session_id":"source-current","hook_event_name":"SessionStart"}`)
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("hook setup: %v: %s", err, out)
			}
			if got := strings.Contains(string(out), "claude plugin update dibs@dibs"); got != tc.warn {
				t.Errorf("actual SessionStart: warning=%v, want %v: %s", got, tc.warn, out)
			}
		})
	}
}

// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/testexec"
)

func TestDoctorInstalledCodexHookTrustIsAProblemThroughCommand(t *testing.T) {
	bin := t.TempDir()
	name := "codex"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	codex := filepath.Join(bin, name)
	if out, err := exec.Command("go", "build", "-o", codex, "./testdata/codexplugin").CombinedOutput(); err != nil {
		t.Fatalf("fixture build: %v %s", err, out)
	}
	testexec.PrepareExecutable(t, exec.Command(codex, "--version"))
	board := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/mcp":
			_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`)
		case "/api/board":
			_, _ = io.WriteString(w, `{"node":"doctor-fixture","host_id":"doctor-fixture","agents":[],"claims":[]}`)
		case "/api/client-transports":
			_, _ = io.WriteString(w, `{"scope":"live_connections","clients":[{"id":17,"transport":"plaintext-tcp","mcp":true,"user_agent":"dibs/old (codex)","bridge_pid":4242,"bridge_started_at":"2026-10-29T00:00:00Z"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(board.Close)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"untrusted", "modified", "trusted", "managed", "disabled-plugin"} {
		t.Run(mode, func(t *testing.T) {
			home := t.TempDir()
			ch := filepath.Join(home, ".codex")
			dir := cliIPCProfile(t)
			for _, path := range []string{ch, dir} {
				if err := os.MkdirAll(path, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			enabled := "true"
			if mode == "disabled-plugin" {
				enabled = "false"
			}
			if err := os.WriteFile(filepath.Join(ch, "config.toml"), []byte("[plugins.\"dibs@dibs\"]\nenabled = "+enabled+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "local.secret"), []byte("fixture-secret"), 0o600); err != nil {
				t.Fatal(err)
			}
			startFixtureIPC(t, dir, board.Config.Handler)
			cmd := exec.CommandContext(t.Context(), exe, "-test.run=^TestDoctorCodexTrustCommandProcess$")
			cmd.Env = append(os.Environ(), "DIBS_TEST_HOOK_DOCTOR=1", "DIBS_TEST_PLUGIN_EXECUTABLE="+codex,
				"DIBS_TEST_PLUGIN_HOME="+ch,
				"DIBS_HOOK_FIXTURE_STATUS="+mode, "DIBS_ADDR="+strings.TrimPrefix(board.URL, "http://"),
				"HOME="+home, "CODEX_HOME="+ch, "DIBS_DIR="+dir, "DIBS_NOTIFY=off", "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			_ = cmd.Run() // other fixture checks may also report problems
			var report doctorReport
			if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
				t.Fatalf("doctor JSON: %v %s; stderr=%s", err, stdout.String(), stderr.String())
			}
			found := false
			for _, check := range report.Checks {
				if !strings.Contains(check.What, "Codex will not run") {
					continue
				}
				found = true
				if check.Level != "problem" || !strings.Contains(check.Fix, "dibs codex-hooks --trust") {
					t.Errorf("enabled installed hooks are not a problem with a corrective call: %+v", check)
				}
			}
			want := mode == "untrusted" || mode == "modified"
			problems := 0
			for _, check := range report.Checks {
				if check.Level == "problem" {
					problems++
				}
			}
			if report.Problems != problems || report.Healthy != (problems == 0) {
				t.Error("doctor summary disagrees with its problem checks")
			}
			if found != want {
				t.Errorf("hook refusal found=%v, want %v; checks=%+v", found, want, report.Checks)
			}
			transportWarn := false
			for _, check := range report.Checks {
				if !strings.Contains(check.What, "plaintext-tcp") {
					continue
				}
				transportWarn = check.Level == "warning" && strings.Contains(check.What, "reported bridge pid 4242") &&
					strings.Contains(check.What, "dibs/old (codex)") && strings.Contains(check.Fix, "restart") &&
					strings.Contains(check.Fix, "harness")
			}
			if !transportWarn {
				t.Errorf("real doctor command omitted the actionable plaintext bridge warning: %+v", report.Checks)
			}
			if mode != "disabled-plugin" {
				if _, err := os.Stat(filepath.Join(ch, "hooks-listed")); err != nil {
					t.Fatal("setup: command never asked the fixture for hook trust:", err)
				}
			}
		})
	}
}

func TestDoctorCodexTrustCommandProcess(t *testing.T) {
	if os.Getenv("DIBS_TEST_HOOK_DOCTOR") != "1" {
		return
	}
	codexPluginExecutable = func() string { return os.Getenv("DIBS_TEST_PLUGIN_EXECUTABLE") }
	// TestMain installs a private default home; this subprocess fixture names
	// its own private home after that startup isolation, just like upgrade's.
	if err := os.Setenv("CODEX_HOME", os.Getenv("DIBS_TEST_PLUGIN_HOME")); err != nil {
		t.Fatal(err)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != os.Getenv("DIBS_ADDR") {
			return nil, errors.New("fixture: no external reads")
		}
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
	http.DefaultTransport = transport
	os.Args = []string{"dibs", "doctor", "--json"}
	main()
	os.Exit(0)
}

func TestActualCurrentDaemonUpgradeReportsHookTrust(t *testing.T) {
	cmd, home := currentDaemonUpgradePluginFixture(t)
	for i, kv := range cmd.Env {
		if strings.HasPrefix(kv, "DIBS_PLUGIN_FIXTURE=") {
			cmd.Env[i] = "DIBS_PLUGIN_FIXTURE=ok"
		}
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("actual upgrade: %v %s", err, out)
	}
	for _, want := range []string{"Codex plugin verified", "dibs codex-hooks --trust", "nothing to do"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("actual upgrade omitted %q: %s", want, out)
		}
	}
	if _, err := os.Stat(filepath.Join(home, "installer-called")); err != nil {
		t.Fatal("setup: upgrade never invoked the real installer path:", err)
	}
}

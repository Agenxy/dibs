// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/testexec"
	"github.com/agenxy/dibs/internal/testport"
)

// Two actual boards and the shipped CLI: the original bug was the command's
// diagnosis of another HOME's daemon, not its HTTP decoder in isolation.
func TestCLIDistinguishesForeignBoardFromChangedSecret(t *testing.T) {
	bin := t.TempDir()
	cli, daemon := filepath.Join(bin, "dibs"), filepath.Join(bin, "dibd")
	if runtime.GOOS == "windows" {
		cli, daemon = cli+".exe", daemon+".exe"
	}
	for _, build := range []struct{ path, source string }{{cli, "."}, {daemon, "../dibd"}} {
		cmd := exec.CommandContext(t.Context(), "go", "build", "-o", build.path, build.source)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build command fixture: %v %s", err, out)
		}
		prepare := exec.Command(build.path, "--help")
		prepare.Env = diagnosticFixtureEnv(t.TempDir(), "", "")
		testexec.PrepareExecutable(t, prepare)
	}
	firstHome, secondHome := cliIPCProfile(t), cliIPCProfile(t)
	first, second := filepath.Join(firstHome, ".dibs"), filepath.Join(secondHome, ".dibs")
	firstAddr := startDiagnosticBoard(t, daemon, first)
	secondAddr := startDiagnosticBoard(t, daemon, second)
	secondCA, err := os.ReadFile(filepath.Join(second, "tls-ca.pem"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(first, "trusted-certs.pem"), secondCA, 0o600); err != nil {
		t.Fatal(err)
	}
	firstNode, secondNode := diagnosticNode(t, first), diagnosticNode(t, second)
	if firstNode == secondNode {
		t.Fatal("setup: two boards have the same identity")
	}
	fingerprint := func(node string) string {
		sum := sha256.Sum256([]byte("dibs-board-identity\x00" + node))
		return fmt.Sprintf("%x", sum[:16])
	}
	firstID, secondID := fingerprint(firstNode), fingerprint(secondNode)
	run := func(t *testing.T, address string, args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, cli, args...)
		cmd.Dir, cmd.Env = first, diagnosticFixtureEnv(firstHome, first, address)
		out, err := cmd.CombinedOutput()
		if ctx.Err() != nil {
			t.Fatalf("CLI diagnostic timed out: %v %s", err, out)
		}
		if args[0] == "version" && err != nil {
			t.Fatalf("version must remain informational: %v %s", err, out)
		}
		if args[0] == "doctor" && err == nil {
			t.Fatalf("doctor accepted a rejected credential: %s", out)
		}
		return string(out)
	}
	for _, command := range []string{"version", "doctor"} {
		t.Run(command+"/foreign", func(t *testing.T) {
			out := run(t, secondAddr, command)
			for _, want := range []string{
				"401", "a different board is answering at " + secondAddr,
				"not the board in ~/.dibs", firstID[:8], secondID[:8], "DIBS_ADDR", "DIBS_DIR",
			} {
				if !strings.Contains(out, want) {
					t.Errorf("missing %q: %s", want, out)
				}
			}
			for _, line := range strings.Split(out, "\n") {
				if strings.Contains(line, "a different board is answering") {
					lead, _, _ := strings.Cut(line, "(")
					if strings.Contains(lead, firstID[:8]) || strings.Contains(lead, secondID[:8]) {
						t.Errorf("foreign-board lead names fingerprints instead of address/directory: %s", out)
					}
				}
			}
			for _, falseAdvice := range []string{"nothing listening", "not running", "recreated", "restart", firstNode, secondNode} {
				if strings.Contains(strings.ToLower(out), strings.ToLower(falseAdvice)) {
					t.Errorf("foreign board disclosed identity or gave false advice %q: %s", falseAdvice, out)
				}
			}
		})
	}
	// The node stays the same while the daemon retains its original secret.
	if err := os.WriteFile(filepath.Join(first, "local.secret"), []byte("replacement-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"version", "doctor"} {
		t.Run(command+"/matching", func(t *testing.T) {
			out := run(t, firstAddr, command)
			for _, want := range []string{
				"401", "credential mismatch", "~/.dibs", firstAddr,
				"restart this board's daemon", "dibs stop", "dibd",
			} {
				if !strings.Contains(out, want) {
					t.Errorf("missing %q: %s", want, out)
				}
			}
			if strings.Contains(out, "different board") || strings.Contains(out, "recreated") || strings.Contains(out, "nothing listening") {
				t.Errorf("matching identity misdiagnosed: %s", out)
			}
		})
	}
	// An older daemon has no fingerprint. Do not turn absence into evidence of
	// either cause, or tell someone to restart a board we cannot identify.
	old := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer old.Close()
	trustDiagnosticServer(t, first, old)
	for _, command := range []string{"version", "doctor"} {
		t.Run(command+"/unknown", func(t *testing.T) {
			out := run(t, old.URL, command)
			for _, want := range []string{"401", "cannot identify", "different board", "credential mismatch", "DIBS_ADDR", "DIBS_DIR"} {
				if !strings.Contains(out, want) {
					t.Errorf("missing %q: %s", want, out)
				}
			}
			if strings.Contains(strings.ToLower(out), "restart") || strings.Contains(out, "nothing listening") {
				t.Errorf("unknown board has unsafe/false advice: %s", out)
			}
		})
	}
	for _, status := range []int{http.StatusInternalServerError, http.StatusOK} {
		t.Run(fmt.Sprintf("version/http-%d", status), func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
				_, _ = w.Write([]byte("not a discovery reply"))
			}))
			defer server.Close()
			trustDiagnosticServer(t, first, server)
			out := run(t, server.URL, "version")
			if !strings.Contains(out, "version unavailable") || strings.Contains(out, "nothing listening") ||
				strings.Contains(out, "not running") {
				t.Fatalf("HTTP answer was reported as an absent daemon: %s", out)
			}
			if status != http.StatusOK && !strings.Contains(out, "500") {
				t.Fatalf("HTTP refusal lost its status: %s", out)
			}
		})
	}
}

func diagnosticFixtureEnv(home, dir, address string) []string {
	if address != "" && !strings.Contains(address, "://") {
		address = "https://" + address
	}
	env := make([]string, 0, len(os.Environ()))
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "DIBS_") || key == "HOME" || key == "USERPROFILE" ||
			key == "XDG_CONFIG_HOME" || key == "XDG_DATA_HOME" || key == "CODEX_HOME" {
			continue
		}
		env = append(env, entry)
	}
	return append(env, "HOME="+home, "USERPROFILE="+home, "DIBS_DIR="+dir, "DIBS_ADDR="+address,
		"DIBS_TRANSPORT=https",
		"XDG_CONFIG_HOME="+filepath.Join(home, "config"), "XDG_DATA_HOME="+filepath.Join(home, "data"),
		"CODEX_HOME="+filepath.Join(home, "codex"), "DIBS_ADMIN=1")
}

func diagnosticNode(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "node_id"))
	if err != nil || strings.TrimSpace(string(data)) == "" {
		t.Fatalf("setup: board has no identity: %v", err)
	}
	return strings.TrimSpace(string(data))
}

func startDiagnosticBoard(t *testing.T, binary, home string) string {
	t.Helper()
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	port := testport.Reserve(t, "tcp", "127.0.0.1:0")
	logPath := filepath.Join(home, "daemon.log")
	log, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	port.Release(t)
	cmd := exec.CommandContext(t.Context(), binary, "-allow-parallel", "-dir", home, "-addr", "https://"+port.Addr)
	cmd.Dir, cmd.Env = home, diagnosticFixtureEnv(home, home, port.Addr)
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		_ = log.Close()
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		if err := <-done; err != nil {
			t.Logf("diagnostic board exited after kill: %v", err)
		}
		_ = log.Close()
	})
	client := &http.Client{Timeout: time.Second}
	defer client.CloseIdleConnections()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			done <- err
			out, _ := os.ReadFile(logPath)
			t.Fatalf("setup: daemon exited: %v %s", err, out)
		default:
		}
		ca, err := os.ReadFile(filepath.Join(home, "tls-ca.pem"))
		if err != nil {
			time.Sleep(10 * time.Millisecond)
			continue
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(ca) {
			t.Fatal("setup: invalid diagnostic board CA")
		}
		client.Transport = &http.Transport{TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS13, RootCAs: pool,
		}}
		resp, err := client.Get("https://" + port.Addr + "/livez")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return port.Addr
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	out, _ := os.ReadFile(logPath)
	t.Fatalf("setup: daemon did not become ready: %s", out)
	return ""
}

func trustDiagnosticServer(t *testing.T, dir string, server *httptest.Server) {
	t.Helper()
	cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	f, err := os.OpenFile(filepath.Join(dir, "trusted-certs.pem"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(cert); err != nil {
		t.Error(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

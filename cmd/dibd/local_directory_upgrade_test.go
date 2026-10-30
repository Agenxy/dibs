// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

//go:build darwin || linux

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/testport"
)

func TestDaemonStartupTightensOnlySafeExistingDirectories(t *testing.T) {
	for _, mode := range []os.FileMode{0o755, 0o750, 0o711} {
		t.Run(fmt.Sprintf("%04o", mode), func(t *testing.T) {
			dir := qShortDir(t)
			if err := os.Chmod(dir, mode); err != nil {
				t.Fatal(err)
			}
			_, stop, logs := qStartBoardCaptured(t, dir)
			stop()
			info, err := os.Stat(dir)
			if err != nil || info.Mode().Perm() != 0o700 {
				t.Fatalf("ready daemon did not tighten its existing directory: %v %v", info, err)
			}
			want := fmt.Sprintf("old_mode=%04o", mode)
			if strings.Count(logs(), "tightened private data directory") != 1 ||
				!strings.Contains(logs(), "level=WARN") || !strings.Contains(logs(), dir) ||
				!strings.Contains(logs(), want) || !strings.Contains(logs(), "new_mode=0700") {
				t.Fatalf("startup lost its explicit tightening warning: %s", logs())
			}
		})
	}
}

func TestDaemonStartupRefusesWritableDirectoryWithLiteralRepair(t *testing.T) {
	dir := qShortDir(t)
	if err := os.Chmod(dir, 0o775); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestQDaemonChild$")
	port := testport.Reserve(t, "tcp", "127.0.0.1:0")
	cmd.Env = append(nativeProbeEnv(t.TempDir(), "", ""),
		"DIBS_Q_DAEMON_CHILD=1", "DIBS_DIR="+dir, "DIBS_ADDR="+port.Addr)
	port.Release(t)
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil || err == nil || !strings.Contains(string(out), "chmod 700 '") ||
		!strings.Contains(string(out), dir) {
		t.Fatalf("unsafe directory refusal lost its corrective command: %v %s", err, out)
	}
	info, err := os.Stat(dir)
	if err != nil || info.Mode().Perm() != 0o775 {
		t.Fatalf("refusal changed an untrusted directory: %v %v", info, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "local.secret")); !os.IsNotExist(err) {
		t.Fatalf("refusal created a credential: %v", err)
	}
}

func TestDaemonStartupDoesNotTightenAroundAnExposedKey(t *testing.T) {
	for _, name := range []string{"key", "local.secret", "tls-key.pem", "tls-ca-key.pem", "guest-ca-key.pem", "acme-cache/account"} {
		t.Run(name, func(t *testing.T) { qRefuseExposedStoredKey(t, name) })
	}
}

func qRefuseExposedStoredKey(t *testing.T, name string) {
	t.Helper()
	dir := qShortDir(t)
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(key), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(key, []byte("fixture-key-never-production"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestQDaemonChild$")
	port := testport.Reserve(t, "tcp", "127.0.0.1:0")
	cmd.Env = append(nativeProbeEnv(t.TempDir(), "", ""),
		"DIBS_Q_DAEMON_CHILD=1", "DIBS_DIR="+dir, "DIBS_ADDR="+port.Addr)
	port.Release(t)
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil || err == nil || !strings.Contains(string(out), "chmod 600 '") ||
		!strings.Contains(string(out), key) || strings.Contains(string(out), "fixture-key-never-production") {
		t.Fatalf("exposed key did not stop startup with a safe corrective: %v %s", err, out)
	}
	info, err := os.Stat(dir)
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("startup repaired around an untrusted key: %v %v", info, err)
	}
}

func TestDaemonDoesNotTightenAroundAnExposedConfiguredTLSKey(t *testing.T) {
	dir := qShortDir(t)
	addr, stop := qStartBoard(t, dir)
	stop()
	cert, key, err := ensureSelfSignedCert(qShortDir(t), "127.0.0.1:4777")
	if err != nil {
		t.Fatal("setup: create a real valid TLS pair:", err)
	}
	config := fmt.Sprintf("tls_cert = %q\ntls_key = %q\n[wake]\nsockets = false\n", cert, key)
	if err := os.WriteFile(filepath.Join(dir, "dibs.toml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(key, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, check := range []bool{true, false} {
		out, err := qDirectoryChild(t, dir, addr, check)
		if err == nil || !strings.Contains(string(out), "chmod 600 '") || !strings.Contains(string(out), key) {
			t.Fatalf("exposed configured key passed real daemon (check=%v): %v %s", check, err, out)
		}
		info, err := os.Stat(dir)
		if err != nil || info.Mode().Perm() != 0o755 {
			t.Fatalf("daemon tightened around an exposed configured key: %v %v", info, err)
		}
	}
	if err := os.Chmod(key, 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := qDirectoryChild(t, dir, addr, true); err != nil || !strings.Contains(string(out), " replays ") {
		t.Fatalf("legitimate configured key failed replay preflight: %v %s", err, out)
	}
}

func qDirectoryChild(t *testing.T, dir, addr string, check bool) ([]byte, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestQDaemonChild$")
	cmd.Env = append(nativeProbeEnv(t.TempDir(), "", ""), "DIBS_Q_DAEMON_CHILD=1", "DIBS_DIR="+dir, "DIBS_ADDR="+addr)
	if check {
		cmd.Env = append(cmd.Env, "DIBS_Q_CHECK=1")
	}
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("daemon did not reach its permission decision: %v %s", ctx.Err(), out)
	}
	return out, err
}

func TestDaemonReplayPreflightRefusesExposedStoredTLSKeys(t *testing.T) {
	for _, name := range []string{"tls-key.pem", "tls-ca-key.pem", "guest-ca-key.pem", "acme-cache/account"} {
		t.Run(name, func(t *testing.T) {
			dir := qShortDir(t)
			addr, stop := qStartBoard(t, dir)
			stop()
			path := filepath.Join(dir, name)
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			const contents = "fixture-key-not-a-live-secret"
			if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
				t.Fatal(err)
			}
			out, err := qDirectoryChild(t, dir, addr, true)
			if err == nil || !strings.Contains(string(out), "chmod 600 '") ||
				!strings.Contains(string(out), path) || strings.Contains(string(out), contents) {
				t.Fatalf("replay preflight admitted an exposed stored key: %v %s", err, out)
			}
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != 0o644 {
				t.Fatalf("preflight changed key metadata: %v %v", info, err)
			}
		})
	}
}

func TestDoctorIPCPreflightReportsBeforeCutoverAndAcknowledgesTightening(t *testing.T) {
	bin := qBuildCLI(t)
	dir := qShortDir(t)
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := qClient(t, bin, dir, "127.0.0.1:1", "", "doctor", "--ipc-preflight", "--json")
	if err != nil || !strings.Contains(string(out), "will be tightened from 0755") {
		t.Fatalf("staged preflight did not report the upcoming change offline: %v %s", err, out)
	}
	info, err := os.Stat(dir)
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("staged preflight changed the directory: %v %v", info, err)
	}
	_, stop := qStartBoard(t, dir)
	stop()
	out, err = qClient(t, bin, dir, "127.0.0.1:1", "", "doctor", "--ipc-preflight", "--json")
	if err != nil || !strings.Contains(string(out), "tightened from 0755 at ") ||
		strings.Contains(string(out), qFixtureSecret) {
		t.Fatalf("doctor lost the real startup notice: %v %s", err, out)
	}
	out, err = qClient(t, bin, dir, "127.0.0.1:1", "", "doctor", "--ipc-preflight", "--json")
	if err != nil || strings.Contains(string(out), "tightened from 0755 at ") {
		t.Fatalf("clean preflight did not acknowledge the derived notice: %v %s", err, out)
	}
}

func TestDaemonReplayPreflightDoesNotTightenTheRunningBoardsDirectory(t *testing.T) {
	dir := qShortDir(t)
	addr, stop := qStartBoard(t, dir)
	stop()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestQDaemonChild$")
	cmd.Env = append(nativeProbeEnv(t.TempDir(), "", ""), "DIBS_Q_DAEMON_CHILD=1", "DIBS_Q_CHECK=1",
		"DIBS_DIR="+dir, "DIBS_ADDR="+addr)
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), " replays ") {
		t.Fatalf("real daemon replay preflight did not succeed: %v %s", err, out)
	}
	info, err := os.Stat(dir)
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("read-only preflight changed the directory: %v %v", info, err)
	}
}

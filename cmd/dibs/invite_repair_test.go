// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/build"
	"github.com/agenxy/dibs/internal/selfupdate"
)

func TestInviteExplicitRepairPublishesOnlyFreshSignatureVerifiedEvidence(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no published guest platform; fixture archive checksums name a published platform")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"invalid-json", "unknown-root", "bad-signature", "fetch-refuses", "signature-refuses"} {
		t.Run(mode, func(t *testing.T) {
			bin, dir := t.TempDir(), t.TempDir()
			probe := filepath.Join(bin, "dibs-repair-probe")
			for _, name := range []string{probe, filepath.Join(bin, "cosign")} {
				if err := os.WriteFile(name, data, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			record := []byte("{")
			if mode == "unknown-root" || mode == "bad-signature" {
				r := map[string]any{"tag": "v0.0.9", "checksums": []byte("altered"), "bundle": []byte("old signature")}
				if mode == "unknown-root" {
					r["trusted_root"] = "not authority"
				}
				record, err = json.Marshal(r)
				if err != nil {
					t.Fatal(err)
				}
			}
			cache := filepath.Join(dir, "guest-release.json")
			if err := os.WriteFile(cache, record, 0o600); err != nil {
				t.Fatal(err)
			}
			// Explicit verification must remain separate from daemon auth/config.
			if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("invalid = ["), 0o600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, probe, "-test.run=^TestCLIInviteRepairProcess$", "--", "invite", "--verify-release", "v0.0.9")
			cmd.Env = []string{
				"DIBS_TEST_INVITE_REPAIR=" + mode, "DIBS_TEST_UPGRADE_COSIGN=1",
				"DIBS_DIR=" + dir, "PATH=" + bin, "DIBS_TOKEN=fake-token-must-not-be-sent",
			}
			if mode == "signature-refuses" {
				cmd.Env = append(cmd.Env, "DIBS_TEST_UPGRADE_SIGNATURE_REFUSE=1")
			}
			for _, key := range []string{"TMPDIR", "TEMP", "SystemRoot", "SYSTEMROOT"} {
				if value, ok := os.LookupEnv(key); ok {
					cmd.Env = append(cmd.Env, key+"="+value)
				}
			}
			out, err := cmd.CombinedOutput()
			failure := mode == "fetch-refuses" || mode == "signature-refuses"
			if (err != nil) != failure || !strings.Contains(string(out), "Retained release evidence was refused:") {
				t.Fatalf("explicit repair did not honestly diagnose/verify: %v %s", err, out)
			}
			got, err := os.ReadFile(cache)
			if err != nil {
				t.Fatal(err)
			}
			if failure {
				if string(got) != string(record) {
					t.Fatal("failed acquisition/signature replaced old evidence")
				}
			} else {
				var r struct {
					Tag               string
					Checksums, Bundle []byte
				}
				if err := json.Unmarshal(got, &r); err != nil || r.Tag != "v0.0.9" ||
					string(r.Checksums) != upgradeFixtureChecksums() || string(r.Bundle) != "fixture signed bundle\n" {
					t.Fatalf("cache was not freshly verified exact evidence: %+v %v", r, err)
				}
				if !strings.Contains(string(out), "actual board build remains devel") || !strings.Contains(string(out), "INCOMPLETE") {
					t.Fatal("explicit repair claimed readiness or relabelled the board")
				}
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 2 {
				t.Fatalf("explicit repair initialized or otherwise mutated board state: %v %v", entries, err)
			}
		})
	}
}

type inviteRepairTransport struct{}

func (inviteRepairTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if os.Getenv("DIBS_TEST_INVITE_REPAIR") == "fetch-refuses" {
		return nil, fmt.Errorf("fresh fixture acquisition refused")
	}
	return (upgradeRecordTransport{}).RoundTrip(req)
}

func TestCLIInviteRepairProcess(t *testing.T) {
	if os.Getenv("DIBS_TEST_INVITE_REPAIR") == "" {
		return
	}
	selfupdate.GuestSupportingMinimum = "v0.0.9" // TEST-ONLY, not production support
	build.Version = "devel"
	http.DefaultTransport = inviteRepairTransport{}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Args = append([]string{"dibs"}, os.Args[i+1:]...)
			main()
			os.Exit(0)
		}
	}
	t.Fatal("missing CLI repair argument separator")
}

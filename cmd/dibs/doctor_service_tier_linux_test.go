// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLinuxDoctorMissingServiceDependsOnInstallAndServing(t *testing.T) {
	for _, packaged := range []bool{false, true} {
		for _, serving := range []bool{false, true} {
			name := "plain"
			if packaged {
				name = "homebrew"
			}
			if serving {
				name += "-answering"
			}
			t.Run(name, func(t *testing.T) {
				f := newSystemdInstallFixture(t)
				if !packaged {
					bin := filepath.Join(f.home, "go", "bin")
					if err := os.MkdirAll(bin, 0o700); err != nil {
						t.Fatal(err)
					}
					for _, file := range []string{"dibs", "dibd"} {
						data, err := os.ReadFile(filepath.Join(filepath.Dir(f.cli), file))
						if err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(filepath.Join(bin, file), data, 0o700); err != nil {
							t.Fatal(err)
						}
					}
					f.cli = filepath.Join(bin, "dibs")
				}
				cmd := f.command(t.Context(), "doctor", "--json")
				if serving {
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						w.Header().Set("Content-Type", "application/json")
						body := map[string]any{}
						if r.URL.Path == "/mcp" {
							body = map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"tools": []any{}}}
						}
						_ = json.NewEncoder(w).Encode(body)
					}))
					defer server.Close()
					if err := os.WriteFile(filepath.Join(f.dir, "local.secret"), []byte("fixture-secret\n"), 0o600); err != nil {
						t.Fatal(err)
					}
					cmd.Env = append(cmd.Env, "DIBS_ADDR="+server.URL)
				}
				out, runErr := cmd.Output()
				var report doctorReport
				if err := json.Unmarshal(out, &report); err != nil {
					t.Fatalf("doctor failed outside JSON: %v; run=%v %s", err, runErr, out)
				}
				answerObserved := false
				for _, check := range report.Checks {
					answerObserved = answerObserved || strings.HasPrefix(check.What, "daemon answering on ")
				}
				if answerObserved != serving {
					t.Fatalf("setup: doctor serving=%v, want %v: %s", answerObserved, serving, out)
				}
				want := "warning"
				if packaged && !serving {
					want = "problem"
				}
				for _, check := range report.Checks {
					if check.What != "Dibs is installed but no service is registered" {
						continue
					}
					if check.Level != want || !strings.Contains(check.Fix, "dibs configure --service") {
						t.Fatalf("wrong service tier/corrective: %+v; want %s", check, want)
					}
					if !packaged && !serving && !strings.Contains(check.Fix, "or start dibd") {
						t.Fatalf("manual setup omits direct start: %+v", check)
					}
					if serving && !strings.Contains(check.Fix, "logout or reboot") {
						t.Fatalf("live manual daemon warning omits persistence boundary: %+v", check)
					}
					return
				}
				t.Fatalf("missing service diagnosis: %s", out)
			})
		}
	}
}

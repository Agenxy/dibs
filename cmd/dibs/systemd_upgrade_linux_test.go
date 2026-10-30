// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Enter at upgrade's discovery of an actual unit, then drive the same
// preflight/cutover phases. Only stopping/starting systemd and the board reply
// are replaced: this tests file repair and its ordering, not a user-bus launch.
func TestLinuxUpgradeRepairsOnlyRecognizedServiceUnits(t *testing.T) {
	for _, kind := range []string{"legacy-cask", "legacy-shell", "managed", "tuned", "tampered", "other-board"} {
		t.Run(kind, func(t *testing.T) {
			f := newSystemdInstallFixture(t)
			oldDaemon := filepath.Join(f.prefix, "Caskroom", "dibs", "0.0.16", "dibd")
			f.stage(t, "0.0.17")
			if err := os.RemoveAll(filepath.Dir(oldDaemon)); err != nil {
				t.Fatal(err)
			}
			searchPath := f.brewPath
			if kind == "legacy-shell" {
				searchPath = "/custom/bin:/usr/bin:/bin"
			}
			old := frozenLegacySystemdFixture(oldDaemon, f.dir, searchPath)
			if kind == "managed" || kind == "tampered" || kind == "other-board" {
				body := systemdBody(oldDaemon, f.dir, "/old/search/path")
				old = systemdTestReceipt(body)
			}
			if kind == "tuned" || kind == "tampered" {
				old += "\n[Service]\nNice=7\n"
			}
			if kind == "other-board" {
				body := systemdBody(oldDaemon, filepath.Join(f.home, "another-board"), "/old/search/path")
				old = systemdTestReceipt(body)
			}
			if err := os.MkdirAll(filepath.Dir(f.target), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(f.target, []byte(old), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("HOME", f.home)
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(f.home, ".config"))
			t.Setenv("DIBS_DIR", f.dir)
			t.Setenv("PATH", filepath.Dir(f.cli)+":/usr/bin:/bin")
			p := &plan{dir: f.dir, installed: filepath.Join(filepath.Dir(f.cli), "dibd"), serving: true}
			p.findDrift()
			if kind == "other-board" {
				if p.unit != "" || p.unitWrong {
					t.Fatal("upgrade selected another board's service")
				}
				if f.unit(t) != old {
					t.Fatal("another board's unit changed during discovery")
				}
				return
			}
			if p.unit != f.target || p.pinned != oldDaemon || !p.unitWrong {
				t.Fatalf("setup: upgrade did not discover the stale service: %+v", p)
			}
			err := p.preflight()
			if kind == "tuned" || kind == "tampered" {
				if err == nil || !strings.Contains(err.Error(), "nothing has been stopped") || !strings.Contains(err.Error(), "Edit ExecStart") {
					t.Fatalf("tuned service was not refused before stopping: %v", err)
				}
				if f.unit(t) != old {
					t.Fatal("refusal changed the tuned unit")
				}
				return
			}
			if err != nil {
				t.Fatalf("recognised service refused: %v", err)
			}
			stopped, started := false, false
			p.stop = func(string) error {
				stopped = true
				if f.unit(t) != old {
					t.Error("service changed before the old daemon stopped")
				}
				return nil
			}
			p.start = func(_, _, _ string, _ daemonState) error {
				started = true
				if !stopped || unitBinary(f.target) != f.stable || !generatedSystemdUnit(f.unit(t)) {
					t.Error("restart reached before a verified stable-link repair")
				}
				return nil
			}
			p.confirm = func(string) error { return nil }
			if err := p.cutover(); err != nil || !started {
				t.Fatalf("cutover failed: %v; started=%v", err, started)
			}
			info, err := os.Stat(f.target)
			if err != nil || info.Mode().Perm() != 0o600 {
				t.Fatalf("repair lost the old permissions: %v", err)
			}
		})
	}
}

func systemdTestReceipt(body string) string {
	return fmt.Sprintf("# written by dibs configure --service; sha256 of the rest: %x\n%s", sha256.Sum256([]byte(body)), body)
}

func TestLinuxDoctorServiceSetupAndRepairHints(t *testing.T) {
	for _, kind := range []string{"missing", "managed-dead", "tuned-dead"} {
		t.Run(kind, func(t *testing.T) {
			f := newSystemdInstallFixture(t)
			if kind != "missing" {
				oldDaemon := filepath.Join(f.prefix, "Caskroom", "dibs", "0.0.16", "dibd")
				body := systemdTestReceipt(systemdBody(oldDaemon, f.dir, "/old/bin"))
				if kind == "tuned-dead" {
					body += "\n[Service]\nNice=7\n"
				}
				f.stage(t, "0.0.17")
				if err := os.RemoveAll(filepath.Dir(oldDaemon)); err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(filepath.Dir(f.target), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(f.target, []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if kind != "missing" {
				if err := os.WriteFile(filepath.Join(f.dir, "local.secret"), []byte("fixture-secret\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			// The missing-service case has no secret yet, just like a first install.
			// The dead cases reach the separate managed-versus-tuned corrective.
			out, err := f.command(t.Context(), "doctor", "--json").Output()
			if err == nil {
				t.Fatal("doctor accepted missing or dead service")
			}
			var report doctorReport
			if err := json.Unmarshal(out, &report); err != nil {
				t.Fatalf("doctor output: %v %s", err, out)
			}
			what, fix := "the service executable does not exist", "dibs upgrade"
			if kind == "missing" {
				what, fix = "Dibs is installed but no service is registered", "dibs configure --service"
			} else if kind == "tuned-dead" {
				fix = "Edit ExecStart"
			}
			for _, check := range report.Checks {
				if check.What == what && check.Level == "problem" && strings.Contains(check.Fix, fix) {
					return
				}
			}
			t.Fatalf("doctor did not report %q with %q: %s", what, fix, out)
		})
	}
}

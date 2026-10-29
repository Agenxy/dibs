// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/testexec"
)

// Enter at main/configure in a separate process, under the staged layout and
// stripped PATH Homebrew actually gives its postflight. No systemd is started.
func TestLinuxConfigureServiceSurvivesReinstall(t *testing.T) {
	f := newSystemdInstallFixture(t)
	f.configure(t)
	first := f.unit(t)
	if pinned := unitBinary(f.target); pinned != f.stable {
		t.Errorf("service pins %q, want version-stable link %q", pinned, f.stable)
	}
	if strings.Contains(first, "Caskroom") || strings.Contains(first, "shims") {
		t.Errorf("brew-private install environment persisted in unit:\n%s", first)
	}
	f.configure(t)
	if second := f.unit(t); first != second {
		t.Fatal("identical configure changed the unit")
	}
	// A new staged candidate and package-manager link, with the old version
	// deleted, is what brew upgrade leaves behind.
	old := filepath.Dir(f.cli)
	f.stage(t, "0.0.17")
	if err := os.RemoveAll(old); err != nil {
		t.Fatal(err)
	}
	f.configure(t)
	if _, err := os.Stat(unitBinary(f.target)); err != nil {
		t.Fatalf("unit stopped resolving after package-manager upgrade: %v", err)
	}
	if got := f.unit(t); got != first {
		t.Fatal("stable managed unit changed across an ordinary upgrade")
	}
}

func TestLinuxConfigureServicePreservesTuningAndReportsDeadBinary(t *testing.T) {
	f := newSystemdInstallFixture(t)
	f.configure(t)
	tuned := f.unit(t) + "\n[Service]\nNice=7\n"
	if err := os.WriteFile(f.target, []byte(tuned), 0o600); err != nil {
		t.Fatal(err)
	}
	out := f.configure(t)
	if got := f.unit(t); got != tuned || !strings.Contains(out, "preserved") {
		t.Fatalf("tuning was changed or silently ignored: %s\n%s", got, out)
	}
	if err := os.Remove(f.stable); err != nil {
		t.Fatal(err)
	}
	out = f.configure(t)
	for _, want := range []string{"does not exist", f.stable, "configure --service"} {
		if !strings.Contains(out, want) {
			t.Errorf("dead preserved unit missing corrective %q: %s", want, out)
		}
	}
	if got := f.unit(t); got != tuned {
		t.Fatal("dead tuned unit was overwritten")
	}
	f.assertDeadServiceProblem(t, f.stable)
}

func TestLinuxDoctorReportsDeadServiceAsProblem(t *testing.T) {
	f := newSystemdInstallFixture(t)
	f.configure(t)
	missing := filepath.Join(f.home, "removed", "dibd")
	unit := "[Service]\nExecStart=\"" + missing + "\" -dir \"" + f.dir + "\"\n"
	if err := os.WriteFile(f.target, []byte(unit), 0o600); err != nil {
		t.Fatal(err)
	}
	f.assertDeadServiceProblem(t, missing)
}

func (f *systemdInstallFixture) assertDeadServiceProblem(t *testing.T, missing string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.dir, "local.secret"), []byte("private-fixture-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	cmd := f.command(ctx, "doctor", "--json")
	data, err := cmd.Output()
	if err == nil {
		t.Fatal("doctor accepted a service whose executable was removed")
	}
	var report doctorReport
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatalf("doctor JSON: %v %s", err, data)
	}
	found := false
	for _, check := range report.Checks {
		if check.What == "the service executable does not exist" {
			found = true
			if check.Level != "problem" || !strings.Contains(check.Fix, missing) ||
				!strings.Contains(check.Fix, "configure --service") {
				t.Errorf("dead executable was not an actionable problem: %+v", check)
			}
		}
	}
	if !found {
		t.Fatalf("doctor never diagnosed the dead executable: %s", data)
	}
}

func TestLinuxGeneratedUnitDoesNotAuthorizeABoardMove(t *testing.T) {
	f := newSystemdInstallFixture(t)
	f.configure(t)
	original := f.unit(t)
	other := filepath.Join(f.home, "other-board")
	t.Setenv("HOME", f.home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(f.home, ".config"))
	t.Setenv("DIBS_DIR", other)
	t.Setenv("PATH", filepath.Dir(f.cli))
	if err := writeServiceUnit(); err != nil {
		t.Fatal(err)
	}
	if got := f.unit(t); got != original {
		t.Fatal("generated receipt authorized a different board")
	}
	if _, err := os.Stat(other); !os.IsNotExist(err) {
		t.Fatalf("preserving another board's unit created its replacement directory: %v", err)
	}
}

func TestLinuxConfigureServiceMigratesOnlyExactLegacyTemplate(t *testing.T) {
	for _, tc := range []struct {
		name    string
		shell   bool
		missing bool
		current bool
	}{
		{"postflight", false, false, false},
		{"shell-old-present", true, false, false},
		{"shell-old-missing", true, true, false},
		{"shell-current", true, false, true},
	} {
		for _, tuned := range []bool{false, true} {
			t.Run(tc.name+fmt.Sprint(tuned), func(t *testing.T) {
				f := newSystemdInstallFixture(t)
				legacyDaemon := filepath.Join(f.prefix, "Caskroom", "dibs", "0.0.16", "dibd")
				if !tc.current {
					f.stage(t, "0.0.17")
				}
				if tc.missing {
					if err := os.Remove(legacyDaemon); err != nil {
						t.Fatal(err)
					}
				}
				oldPath := f.brewPath
				if tc.shell {
					oldPath = "/custom/bin:" + filepath.Join(f.prefix, "bin") + ":/usr/bin:/bin"
				}
				// Frozen v0.0.16 bytes, including the brew shim PATH. Do not produce
				// these with the current generator: that would test only itself.
				legacy := "[Unit]\nDescription=Dibs: coordination board for AI agents\n" +
					"Documentation=https://github.com/agenxy/dibs\nAfter=network.target\n\n" +
					"[Service]\nType=simple\nEnvironment=\"PATH=" + oldPath + "\"\n" +
					"ExecStart=\"" + legacyDaemon + "\" -dir \"" + f.dir + "\"\n" +
					"Restart=on-failure\nRestartSec=2\n\n[Install]\nWantedBy=default.target\n"
				if tuned {
					legacy += "\n[Service]\nNice=7\n"
				}
				if err := os.MkdirAll(filepath.Dir(f.target), 0o750); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(f.target, []byte(legacy), 0o600); err != nil {
					t.Fatal(err)
				}
				f.configure(t)
				got := f.unit(t)
				preserved := tuned || tc.current
				if preserved && got != legacy {
					t.Fatal("legacy tuning was overwritten")
				}
				if !preserved && (got == legacy || unitBinary(f.target) != f.stable) {
					t.Fatalf("exact old template was not migrated to stable binary:\n%s", got)
				}
			})
		}
	}
}

func TestLinuxPreservationWarningUsesStderr(t *testing.T) {
	f := newSystemdInstallFixture(t)
	f.configure(t)
	tuned := f.unit(t) + "\n[Service]\nNice=7\n"
	if err := os.WriteFile(f.target, []byte(tuned), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := f.command(ctx, "configure", "--service")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.Output()
	if err != nil || !strings.Contains(stderr.String(), "preserved "+f.target) {
		t.Fatalf("warning not on the stream Homebrew displays: %v stdout=%s stderr=%s", err, stdout, stderr.String())
	}
	if got := f.unit(t); got != tuned {
		t.Fatal("warning check changed tuning")
	}
}

func TestLinuxConfigureServiceRefreshesReceiptVerifiedUnit(t *testing.T) {
	f := newSystemdInstallFixture(t)
	// A digest proves these are generated bytes; an unrelated handwritten file
	// with the same name does not. Change only a managed default here.
	f.configure(t)
	_, rest, found := strings.Cut(f.unit(t), "\n")
	if !found {
		t.Fatal("generated unit has no header")
	}
	rest = strings.Replace(rest, "RestartSec=2", "RestartSec=3", 1)
	sum := sha256.Sum256([]byte(rest))
	old := fmt.Sprintf("# written by dibs configure --service; sha256 of the rest: %x\n%s", sum, rest)
	if err := os.WriteFile(f.target, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	f.configure(t)
	if got := f.unit(t); got == old || !strings.Contains(got, "RestartSec=2") {
		t.Fatalf("verified generated unit was not refreshed:\n%s", got)
	}
}

type systemdInstallFixture struct {
	home, dir, prefix, cli, stable, target, brewPath string
}

func newSystemdInstallFixture(t *testing.T) *systemdInstallFixture {
	t.Helper()
	home := t.TempDir()
	prefix := filepath.Join(home, "brew")
	f := &systemdInstallFixture{
		home: home, dir: filepath.Join(home, ".dibs"), prefix: prefix,
		stable: filepath.Join(prefix, "bin", "dibd"),
		target: filepath.Join(home, ".config", "systemd", "user", "dibs.service"),
		brewPath: filepath.Join(prefix, "Homebrew", "Library", "Homebrew", "shims", "shared") +
			":/usr/bin:/bin:/usr/sbin:/sbin",
	}
	f.stage(t, "0.0.16")
	// The suite's process-safety guard only preserves a nested fixture HOME
	// when both directories already exist under the private temporary root.
	if err := os.MkdirAll(f.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *systemdInstallFixture) stage(t *testing.T, version string) {
	t.Helper()
	dir := filepath.Join(f.prefix, "Caskroom", "dibs", version)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	f.cli = filepath.Join(dir, "dibs")
	// A private copy/link gives os.Executable the staged name without another
	// compiler or script. Mounted container fixtures may cross filesystems.
	if err := os.Link(os.Args[0], f.cli); err != nil {
		data, readErr := os.ReadFile(os.Args[0])
		if readErr != nil {
			t.Fatal(readErr)
		}
		if writeErr := os.WriteFile(f.cli, data, 0o700); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	daemon := filepath.Join(dir, "dibd")
	if err := os.WriteFile(daemon, []byte("fixture daemon; never executed\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(f.stable), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(f.stable); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := os.Symlink(daemon, f.stable); err != nil {
		t.Fatal(err)
	}
	prepare := exec.Command(f.cli, "--dibs-test-prepare")
	prepare.Env = f.env()
	testexec.PrepareExecutable(t, prepare)
}

func (f *systemdInstallFixture) env() []string {
	return []string{
		"HOME=" + f.home, "DIBS_DIR=" + f.dir, "DIBS_ADDR=http://127.0.0.1:1",
		"XDG_CONFIG_HOME=" + filepath.Join(f.home, ".config"), "CODEX_HOME=" + filepath.Join(f.home, "codex"),
		"PATH=" + f.brewPath, "DIBS_ADMIN=1", "DIBS_TEST_SYSTEMD_CONFIGURE=1", "GORACE=atexit_sleep_ms=0",
	}
}

func (f *systemdInstallFixture) configure(t *testing.T) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := f.command(ctx, "configure", "--service")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("configure --service failed: %v %s", err, out)
	}
	if !strings.Contains(string(out), f.target) {
		t.Fatalf("setup: command did not use this fixture's service target %s: %s", f.target, out)
	}
	return string(out)
}

func (f *systemdInstallFixture) command(ctx context.Context, args ...string) *exec.Cmd {
	argv := append([]string{"-test.run=^TestSystemdConfigureProcess$", "--"}, args...)
	cmd := exec.CommandContext(ctx, f.cli, argv...)
	cmd.Dir, cmd.Env = f.home, f.env()
	return cmd
}

func (f *systemdInstallFixture) unit(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(f.target)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestSystemdConfigureProcess(t *testing.T) {
	if os.Getenv("DIBS_TEST_SYSTEMD_CONFIGURE") != "1" {
		return
	}
	separator := slices.Index(os.Args, "--")
	if separator < 0 {
		t.Fatal("missing CLI separator")
	}
	os.Args = append([]string{"dibs"}, os.Args[separator+1:]...)
	main()
	os.Exit(0)
}

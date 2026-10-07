// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

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
)

// Enter through the actual CLI and its fetch/current dispatch, with a private
// installed helper. The helper rejects anything except the existing read-only
// mode and records the call; no authorization request or posting is possible.
func TestUpgradeFetchReportsInstalledNotificationPermission(t *testing.T) {
	for _, route := range []string{"fetch", "current-fetch", "current-bare"} {
		current := route != "fetch"
		for _, mode := range []string{"not-determined", "denied", "old", "malformed", "authorized"} {
			t.Run(route+"/"+mode, func(t *testing.T) {
				root := t.TempDir()
				bin := filepath.Join(root, "bin")
				app := filepath.Join(bin, "Dibs.app")
				macos := filepath.Join(app, "Contents", "MacOS")
				if err := os.MkdirAll(macos, 0o700); err != nil {
					t.Fatal(err)
				}
				data, err := os.ReadFile(os.Args[0])
				if err != nil {
					t.Fatal(err)
				}
				probe := filepath.Join(bin, "dibs-upgrade-probe")
				for _, path := range []string{probe, filepath.Join(bin, "cosign"), filepath.Join(macos, "dibs-notify")} {
					if err := os.WriteFile(path, data, 0o700); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.WriteFile(filepath.Join(bin, "dibd"), []byte("old fixture daemon"), 0o700); err != nil {
					t.Fatal(err)
				}
				if route == "current-bare" {
					if err := os.WriteFile(filepath.Join(bin, "dibd"), data, 0o700); err != nil {
						t.Fatal(err)
					}
				}
				// The registrar sees only a private fixture identity, never the
				// installed Dibs identity on the developer's machine.
				id := fmt.Sprintf("org.agenxy.dibs.upgrade-fixture.%d.%d", os.Getpid(), time.Now().UnixNano())
				plist := `<?xml version="1.0"?><plist version="1.0"><dict><key>CFBundleIdentifier</key><string>` + id + `</string><key>CFBundleExecutable</key><string>dibs-notify</string><key>CFBundlePackageType</key><string>APPL</string><key>LSUIElement</key><true/></dict></plist>`
				if err := os.WriteFile(filepath.Join(app, "Contents", "Info.plist"), []byte(plist), 0o600); err != nil {
					t.Fatal(err)
				}
				const registrar = "/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister"
				t.Cleanup(func() { _ = exec.Command(registrar, "-u", app).Run() })
				marker := filepath.Join(root, "status-called")
				status := mode
				if mode == "old" || mode == "malformed" {
					status = "authorized"
				}
				value := fmt.Sprintf(`{"version":1,"authorization_status":%q,"alert_style":"alert","alert_setting":"enabled","notification_center_setting":"enabled","lock_screen_setting":"enabled","time_sensitive_setting":"not-supported","focus":{"authorization":"not-determined","observable":false}}`, status)
				ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
				defer cancel()
				args := []string{"-test.run=^TestCLIUpgradeRecordProcess$", "--", "upgrade"}
				if route != "current-bare" {
					args = append(args, "--fetch", "--dry-run")
				}
				cmd := exec.CommandContext(ctx, probe, args...)
				cmd.Env = append(os.Environ(), "DIBS_TEST_UPGRADE_RECORD=1", "DIBS_TEST_UPGRADE_COSIGN=1", "DIBS_DIR="+root, "PATH="+bin+":/usr/bin:/bin", "DIBS_NOTIFY=", "DIBS_SETTINGS_HELPER_MODE=doctor", "DIBS_SETTINGS_HELPER_MARKER="+marker, "DIBS_SETTINGS_GUARD_MODE="+mode, "DIBS_SETTINGS_GUARD_VALUE="+value)
				if current {
					cmd.Env = append(cmd.Env, "DIBS_TEST_UPGRADE_CURRENT=1")
				}
				if route == "current-bare" {
					cmd.Env = append(cmd.Env, "DIBS_TEST_UPGRADE_BARE=1", "HOME="+root, "DIBS_ADDR=127.0.0.1:49998")
				}
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("actual fixture upgrade failed: %v %s", err, out)
				}
				setup := "fleet has NOT been moved"
				switch route {
				case "current-fetch":
					setup = "Nothing to fetch"
				case "current-bare":
					setup = "nothing to do"
				}
				if !strings.Contains(string(out), setup) {
					t.Fatalf("setup missed requested actual path: %s", out)
				}
				mark, err := os.ReadFile(marker)
				if err != nil || string(mark) != "status" {
					t.Fatalf("upgrade never measured installed helper: %q %v; %s", mark, err, out)
				}
				want := "notification authorization after upgrade: " + mode
				if mode == "old" || mode == "malformed" {
					want = "notification authorization is unknown"
				}
				if mode == "authorized" {
					if strings.Contains(string(out), "warning: notification") {
						t.Fatalf("authorized fixture invented lost permission: %s", out)
					}
				} else if !strings.Contains(string(out), want) || !strings.Contains(string(out), "dibs doctor") {
					t.Fatalf("installed helper state/remedy hidden: want %q, got %s", want, out)
				}
			})
		}
	}
}

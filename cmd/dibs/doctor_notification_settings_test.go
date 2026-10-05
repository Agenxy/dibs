package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Drive doctor's actual notification check, installed-helper lookup and status
// subprocess. An old helper must never be given an unknown positional flag.
func TestDoctorMeasuresNativeNotificationSettings(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("native macOS notification settings")
	}
	if os.Getenv("DIBS_SETTINGS_DOCTOR_DRIVER") == "1" {
		for _, tc := range []struct {
			name, style, sensitive, want string
		}{
			{"banner", "banner", "enabled", "System Settings > Notifications > Dibs > Alerts"},
			{"silent", "none", "enabled", "System Settings > Notifications > Dibs > Alerts"},
			{"disabled-sensitive", "alert", "disabled", "Time Sensitive"},
			{"unprovisioned", "alert", "not-supported", "this build is not provisioned"},
			{"old-helper", "old", "enabled", "settings are unknown"},
			{"malformed-helper", "malformed", "enabled", "settings are unknown"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				settings := map[string]any{
					"version": 1, "authorization_status": "authorized", "alert_style": tc.style,
					"alert_setting": "enabled", "notification_center_setting": "enabled",
					"lock_screen_setting": "enabled", "time_sensitive_setting": tc.sensitive,
					"focus": map[string]any{"authorization": "not-determined", "observable": false},
				}
				raw, err := json.Marshal(settings)
				if err != nil {
					t.Fatal(err)
				}
				t.Setenv("DIBS_SETTINGS_GUARD_VALUE", string(raw))
				t.Setenv("DIBS_SETTINGS_GUARD_MODE", tc.style)
				var lines []string
				line := func(s string) { lines = append(lines, s) }
				checkNotificationRoute(line, line, func(what, fix string) { lines = append(lines, what, fix) })
				got := strings.Join(lines, "\n")
				if !strings.Contains(got, tc.want) {
					t.Fatalf("actual doctor lacks %q: %s", tc.want, got)
				}
				if tc.style != "old" && tc.style != "malformed" && !strings.Contains(got, "Focus is not observable") {
					t.Fatalf("ungranted Focus was not retained as unknown: %s", got)
				}
			})
		}
		return
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bytes, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	// No .test suffix: the child uses a private fixture helper; the production
	// test-notification guard correctly refuses a normal test binary.
	driver := filepath.Join(dir, "settings-driver")
	if err := os.WriteFile(driver, bytes, 0o700); err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(dir, "Dibs.app", "Contents", "MacOS", "dibs-notify")
	if err := os.MkdirAll(filepath.Dir(helper), 0o700); err != nil {
		t.Fatal(err)
	}
	const stub = `#!/usr/bin/env -S uv run --script
# /// script
# requires-python = ">=3.11"
# dependencies = []
# ///
import os, sys
if sys.argv[1:] != ["--status"]:
    sys.exit(3)  # A new positional flag could post on an old helper.
mode = os.getenv("DIBS_SETTINGS_GUARD_MODE")
if mode == "old" or os.getenv("DIBS_NOTIFY_SETTINGS_V1") != "1":
    print("authorized")
elif mode == "malformed":
    print('{"version":1,"alert_style":42}')
else:
    print(os.environ["DIBS_SETTINGS_GUARD_VALUE"])
`
	if err := os.WriteFile(helper, []byte(stub), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, driver, "-test.run=^TestDoctorMeasuresNativeNotificationSettings$") // #nosec G204 -- private copy of this test binary
	cmd.Env = append(os.Environ(), "DIBS_SETTINGS_DOCTOR_DRIVER=1", "DIBS_NOTIFY=")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("real doctor notification route: %v\n%s", err, out)
	}
}

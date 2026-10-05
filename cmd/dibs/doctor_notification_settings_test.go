package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/humanask"
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
			warn                         bool
		}{
			{"banner", "banner", "enabled", "System Settings > Notifications > Dibs > Alerts", true},
			{"silent", "none", "enabled", "System Settings > Notifications > Dibs > Alerts", true},
			{"disabled-sensitive", "alert", "disabled", "Time Sensitive", true},
			{"unprovisioned", "alert", "not-supported", "this build is not provisioned", false},
			{"old-helper", "old", "enabled", "settings are unknown", false},
			{"malformed-helper", "malformed", "enabled", "settings are unknown", true},
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
				warned := false
				notes := 0
				checkNotificationRoute(line, func(s string) { notes++; line(s) },
					func(what, fix string) { warned = true; lines = append(lines, what, fix) })
				got := strings.Join(lines, "\n")
				if warned != tc.warn {
					t.Fatalf("actual doctor warning=%t, want %t: %s", warned, tc.warn, got)
				}
				if tc.name == "unprovisioned" && notes != 1 {
					t.Fatalf("standing provisioning/Focus limits must be a single informational note: %s", got)
				}
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

// The relay invokes actual humanask/notifier code with its private helper, then
// reports through the production HTTP client. A hand-called receipt callback
// would not catch the presenter failing to connect these two halves.
func TestRelayCarriesThePostingHelpersActualSettings(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("native macOS relay presenter")
	}
	if os.Getenv("DIBS_SETTINGS_RELAY_DRIVER") == "1" {
		t.Setenv("DIBS_NOTIFY", "")
		t.Setenv("DIBS_DIR", t.TempDir())
		var mu sync.Mutex
		var receipts []map[string]any
		mux := http.NewServeMux()
		mux.HandleFunc("POST /api/human/delivery", func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer fixture-session" {
				t.Error("receipt lost authenticated relay session")
			}
			var data map[string]any
			if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
				t.Error(err)
			}
			mu.Lock()
			receipts = append(receipts, data)
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		})
		srv := httptest.NewServer(mux)
		defer srv.Close()
		r := newRelay(srv.URL, relayState{Key: "fixture-key", Node: "fixture-node"}, &fakeSigner{}, humanask.Ask)
		r.session = "fixture-session"
		r.handle(engine.HumanNotice{Serial: 7, Type: "request", From: "sender", Body: "private relay settings fixture"})
		mu.Lock()
		defer mu.Unlock()
		for _, data := range receipts {
			if data["state"] != "posted" {
				continue
			}
			settings, ok := data["settings"].(map[string]any)
			if !ok || settings["alert_style"] != "banner" || data["interruption_level"] != "timeSensitive" {
				t.Fatalf("actual relay presenter lost posting helper settings: %v", data)
			}
			return
		}
		t.Fatalf("actual relay presenter never reported OS acceptance: %v", receipts)
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
	driver := filepath.Join(dir, "settings-relay-driver")
	if err := os.WriteFile(driver, bytes, 0o700); err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(dir, "Dibs.app/Contents/MacOS/dibs-notify")
	if err := os.MkdirAll(filepath.Dir(helper), 0o700); err != nil {
		t.Fatal(err)
	}
	const stub = `#!/usr/bin/env -S uv run --script
# /// script
# requires-python = ">=3.11"
# dependencies = []
# ///
import json, os, sys, time
if len(sys.argv)!=7 or os.getenv("DIBS_NOTIFY_ID")!="dibs.msg.fixture-node.7":
    sys.exit(3)
settings={"version":1,"authorization_status":"authorized","alert_style":"banner","alert_setting":"enabled","notification_center_setting":"enabled","lock_screen_setting":"enabled","time_sensitive_setting":"not-supported","focus":{"authorization":"not-determined","observable":False}}
with open(os.environ["DIBS_NOTIFY_RECEIPT"],"w") as f:
    json.dump({"state":"posted","settings":settings,"interruption_level":"timeSensitive"},f)
time.sleep(0.3)
print("Later")
`
	if err := os.WriteFile(helper, []byte(stub), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, driver, "-test.run=^TestRelayCarriesThePostingHelpersActualSettings$") // #nosec G204 -- private copy of this test binary, fixture helper
	cmd.Env = append(os.Environ(), "DIBS_SETTINGS_RELAY_DRIVER=1", "DIBS_NOTIFY=")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("actual relay producer wiring: %v\n%s", err, out)
	}
}

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

	"github.com/agenxy/dibs/internal/core"
)

// The default engine presenter invokes actual humanask and notifier code. Only
// the private OS helper is a fixture; no receipt setter is called by this test.
func TestDesktopSettingsComeFromThePostingHelper(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("default macOS desktop boundary")
	}
	if os.Getenv("DIBS_SETTINGS_DESKTOP_DRIVER") == "1" {
		t.Setenv("DIBS_NOTIFY", "")
		t.Setenv("DIBS_DIR", t.TempDir())
		eng, ctx := testEngine(t)
		human, _, err := eng.HumanAgent(ctx)
		if err != nil || human == "" {
			t.Fatalf("human setup: %q %v", human, err)
		}
		reg, err := eng.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "desktop-requester", Nonce: "desktop-settings-fixture"})
		if err != nil || reg["token"] == nil {
			t.Fatalf("sender setup: %v %v", reg, err)
		}
		token := reg["token"].(string)
		for _, kind := range []string{core.MsgRequest, core.MsgNotify} {
			t.Run(kind, func(t *testing.T) {
				sent, err := eng.Do(ctx, &core.Op{Kind: core.OpSendMessage, Token: token, To: human, MsgType: kind, Body: "desktop settings fixture"})
				serial, ok := sent["msg_serial"].(uint64)
				if err != nil || !ok || serial == 0 || sent["human_route"] != "desktop" {
					t.Fatalf("default desktop dispatch setup: %v %v", sent, err)
				}
				deadline := time.Now().Add(5 * time.Second)
				for {
					read, err := eng.GetMessage(ctx, token, serial)
					if err != nil {
						t.Fatal(err)
					}
					raw, err := json.Marshal(read["human_delivery"])
					if err != nil {
						t.Fatal(err)
					}
					var d struct {
						Receipts map[string]struct {
							Posted   bool           `json:"posted"`
							Settings map[string]any `json:"settings"`
							Level    string         `json:"interruption_level"`
							Shown    string         `json:"shown"`
							Hint     string         `json:"hint"`
						} `json:"receipts"`
					}
					if err := json.Unmarshal(raw, &d); err != nil {
						t.Fatal(err)
					}
					r := d.Receipts["desktop"]
					if r.Posted {
						wantLevel := "active"
						if kind == core.MsgRequest {
							wantLevel = "timeSensitive"
						}
						if r.Settings["alert_style"] != "banner" || r.Level != wantLevel || r.Shown != "unconfirmed" {
							t.Fatalf("posting helper metadata never reached actual read_mail: %s", raw)
						}
						if kind == core.MsgRequest && !strings.Contains(r.Hint, "System Settings > Notifications > Dibs > Alerts") {
							t.Fatalf("approval banner has no persistent-alert hint: %s", raw)
						}
						break
					}
					if time.Now().After(deadline) {
						t.Fatalf("private posting helper never confirmed acceptance: %s", raw)
					}
					time.Sleep(20 * time.Millisecond)
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
	driver := filepath.Join(dir, "desktop-settings-driver")
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
settings={"version":1,"authorization_status":"authorized","alert_style":"banner","alert_setting":"enabled","notification_center_setting":"enabled","lock_screen_setting":"enabled","time_sensitive_setting":"not-supported","focus":{"authorization":"not-determined","observable":False}}
if sys.argv[1:]==["--status"]:
    print(json.dumps(settings) if os.getenv("DIBS_NOTIFY_SETTINGS_V1")=="1" else "authorized")
    sys.exit(0)
if len(sys.argv)<4:
    sys.exit(3)
level="timeSensitive" if len(sys.argv)>4 else "active"
with open(os.environ["DIBS_NOTIFY_RECEIPT"],"w") as f:
    json.dump({"state":"posted","settings":settings,"interruption_level":level},f)
time.sleep(0.3)
if level=="timeSensitive":
    print("Later")
`
	if err := os.WriteFile(helper, []byte(stub), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, driver, "-test.run=^TestDesktopSettingsComeFromThePostingHelper$") // #nosec G204 -- private copied test binary and fixture helper
	cmd.Env = append(os.Environ(), "DIBS_SETTINGS_DESKTOP_DRIVER=1", "DIBS_NOTIFY=")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("actual desktop producer wiring: %v\n%s", err, out)
	}
}

// Compile the exact production Swift source only on the hosted macOS gate and
// enter its additive existing --status mode. This reads a fixture bundle's
// settings without requesting notification/Focus permission or posting.
func TestNativeHelperSettingsModeIsPermissionFree(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("native macOS helper")
	}
	dir := t.TempDir()
	bundle := filepath.Join(dir, "Dibs.app")
	macos := filepath.Join(bundle, "Contents/MacOS")
	if err := os.MkdirAll(macos, 0o700); err != nil {
		t.Fatal(err)
	}
	const plist = `<?xml version="1.0" encoding="UTF-8"?><plist version="1.0"><dict><key>CFBundleIdentifier</key><string>org.agenxy.dibs.settings-fixture</string><key>CFBundleExecutable</key><string>dibs-notify</string><key>CFBundlePackageType</key><string>APPL</string><key>LSUIElement</key><true/></dict></plist>`
	if err := os.WriteFile(filepath.Join(bundle, "Contents/Info.plist"), []byte(plist), 0o600); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(macos, "dibs-notify")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "xcrun", "swiftc", "-j1", "../../internal/notify/app/notify_darwin.swift", "-o", executable) // #nosec G204 -- exact production source and private test bundle
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("native fixture compile setup: %v\n%s", err, out)
	}
	if out, err := exec.CommandContext(ctx, "codesign", "--force", "--sign", "-", bundle).CombinedOutput(); err != nil {
		t.Fatalf("native fixture sign setup: %v\n%s", err, out)
	} // #nosec G204 -- private ad-hoc fixture bundle
	readCtx, stop := context.WithTimeout(context.Background(), 12*time.Second)
	defer stop()
	cmd = exec.CommandContext(readCtx, executable, "--status") // #nosec G204 -- private compiled production helper, existing read-only mode
	cmd.Env = append(os.Environ(), "DIBS_NOTIFY_SETTINGS_V1=1")
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 2 {
			t.Fatalf("native status execution failed: %v\n%s", err, out)
		}
	}
	var settings map[string]any
	if err := json.Unmarshal(out, &settings); err != nil || settings["version"] != float64(1) {
		t.Fatalf("actual native helper omitted versioned settings: %v\n%s", err, out)
	}
	for _, key := range []string{"authorization_status", "alert_style", "alert_setting", "notification_center_setting", "lock_screen_setting", "time_sensitive_setting", "focus"} {
		if settings[key] == nil {
			t.Fatalf("native settings lacks %s: %s", key, out)
		}
	}
	// The temporary ad-hoc identity has no prior Focus grant. Reading it cannot
	// turn missing consent into a known observation or request a prompt.
	focus, ok := settings["focus"].(map[string]any)
	if !ok || focus["observable"] != false || focus["authorization"] == "authorized" {
		t.Fatalf("ungranted fixture Focus must be unknown: %s", out)
	}
}

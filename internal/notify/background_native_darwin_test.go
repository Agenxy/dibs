// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package notify

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/procexec"
)

// The prior feature restored focus after navigation. Its replacement refuses
// every legacy request, including from dormant old bridges. The old-code
// control uses the original native-contact fixture, so it never opens apps.
func TestRetiredNativeNavigationModeNeverOpensOrRestores(t *testing.T) {
	t.Setenv("DIBS_TEST_FORBID_APP_OPEN", "1")
	t.Setenv("DIBS_NOTIFY_SETTINGS_V1", "1") // inherited status mode must not shadow capability
	dir := t.TempDir()
	contents := filepath.Join(dir, "FocusFixture.app", "Contents")
	if err := os.MkdirAll(filepath.Join(contents, "MacOS"), 0o700); err != nil {
		t.Fatal(err)
	}
	plist := `<?xml version="1.0"?><plist version="1.0"><dict><key>CFBundleIdentifier</key><string>org.agenxy.dibs.focus-fixture</string><key>CFBundleExecutable</key><string>dibs-notify</string><key>CFBundlePackageType</key><string>APPL</string></dict></plist>`
	if err := os.WriteFile(filepath.Join(contents, "Info.plist"), []byte(plist), 0o600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(contents, "MacOS", "dibs-notify")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	build := procexec.Command(ctx, "swiftc", "-D", "DIBS_BACKGROUND_OPEN_FIXTURE", "-o", binary, nativePromptSource(t))
	if raw, err := build.CombinedOutput(); err != nil {
		t.Fatalf("native fixture setup: %v\n%s", err, raw)
	}
	if info, err := os.Stat(binary); err != nil || info.Size() == 0 {
		t.Fatalf("native fixture setup artifact: %v %v", info, err)
	}
	prior := map[string]any{"pid": 1, "launch": "prior-epoch", "bundle": "com.apple.Safari"}
	target := map[string]any{"pid": 2, "launch": "target-epoch", "bundle": "com.openai.codex"}
	other := map[string]any{"pid": 3, "launch": "other-epoch", "bundle": "com.anthropic.claudefordesktop"}
	observation := func(app any, counter any, idle any, intervening bool) map[string]any {
		return map[string]any{"app": app, "counter": counter, "idle": idle, "interveningActivation": intervening}
	}
	before := observation(prior, 11, 10, false)
	after := observation(target, 11, 10, false)
	for _, tc := range []struct {
		name, reason        string
		observations        []map[string]any
		target              any
		live, open, restore bool
		attempts            int
	}{
		{"restore", "restore-accepted", []map[string]any{before, after, after, observation(prior, 11, 10, false)}, target, true, true, true, 1},
		{"foreground", "already-foreground", []map[string]any{after}, target, true, true, true, 0},
		{"input", "user-input", []map[string]any{before, observation(target, 12, 0, false)}, target, true, true, true, 0},
		{"autorepeat", "user-input", []map[string]any{before, observation(target, 11, 0, false)}, target, true, true, true, 0},
		{"active-input", "input-unknown-or-active", []map[string]any{observation(prior, 11, 0.01, false)}, target, true, true, true, 0},
		{"unknown-input", "input-unknown-or-active", []map[string]any{observation(prior, nil, nil, false)}, target, true, true, true, 0},
		{"unknown-app", "previous-app-unknown", []map[string]any{observation(nil, 11, 10, false)}, target, true, true, true, 0},
		{"unknown-observation", "observation-unknown", []map[string]any{before, observation(nil, nil, nil, false)}, target, true, true, true, 0},
		{"third-app", "user-app-change", []map[string]any{before, observation(other, 11, 10, false)}, target, true, true, true, 0},
		{"intervening-app", "user-app-change", []map[string]any{before, observation(target, 11, 10, true)}, target, true, true, true, 0},
		{"previous-pid-reused", "previous-app-gone", []map[string]any{before, after}, target, false, true, true, 0},
		{"target-pid-reused", "user-app-change", []map[string]any{before, observation(map[string]any{"pid": 2, "launch": "new-epoch", "bundle": "com.openai.codex"}, 11, 10, false)}, target, true, true, true, 0},
		{"unknown-target", "target-incarnation-unknown", []map[string]any{before}, nil, true, true, true, 0},
		{"boundary-input", "restore-boundary-changed", []map[string]any{before, after, observation(target, 12, 0, false)}, target, true, true, true, 0},
		{"restore-refused", "restore-refused", []map[string]any{before, after, after}, target, true, true, false, 1},
		{"open-failed", "open-failed", []map[string]any{before}, target, true, false, true, 0},
		{"activation-timeout", "activation-timeout", []map[string]any{before}, target, true, true, true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload, err := json.Marshal(map[string]any{"target": tc.target, "observations": tc.observations, "previousLive": tc.live, "openSuccess": tc.open, "restoreSuccess": tc.restore})
			if err != nil {
				t.Fatal(err)
			}

			cmd := procexec.Command(ctx, binary, "--open-background", "codex://threads/focus-fixture")
			cmd.Env = append(os.Environ(), "DIBS_BACKGROUND_OPEN_FIXTURE="+string(payload))
			raw, runErr := cmd.Output()
			var receipt map[string]any
			if err := json.Unmarshal(raw, &receipt); err != nil {
				t.Fatalf("retired-mode receipt: %q %v", raw, err)
			}
			if runErr == nil || receipt["opened"] != false || receipt["reason"] != "retired-navigation" ||
				receipt["fixture_open_count"] != float64(0) || receipt["fixture_restore_count"] != float64(0) {
				t.Fatalf("retired mode touched the desktop: receipt=%v err=%v", receipt, runErr)
			}
		})
	}
}

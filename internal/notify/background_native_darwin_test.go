package notify

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// Compile the shipped helper with only its native-contact factory replaced.
// Each case enters OpenChatGPTBackground, capability negotiation, and the real
// --open-background dispatcher. Neither the current nor old-code proof may
// touch the person's apps: an old helper gets --status only, never a new mode.
func TestNativeBackgroundOpenDecisionsThroughProductionMode(t *testing.T) {
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
	build := exec.CommandContext(ctx, "swiftc", "-D", "DIBS_BACKGROUND_OPEN_FIXTURE", "-o", binary, "app/notify_darwin.swift")
	if raw, err := build.CombinedOutput(); err != nil {
		t.Fatalf("native fixture setup: %v\n%s", err, raw)
	}
	if info, err := os.Stat(binary); err != nil || info.Size() == 0 {
		t.Fatalf("native fixture setup artifact: %v %v", info, err)
	}
	oldHelper, oldOutput, oldLegacy := backgroundHelper, backgroundOpenFixture, backgroundLegacyFixture
	t.Cleanup(func() {
		backgroundHelper, backgroundOpenFixture, backgroundLegacyFixture = oldHelper, oldOutput, oldLegacy
	})
	backgroundHelper = func() string { return binary }
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
			var receipt map[string]any
			modeCalls, statusCalls, legacyCalls := 0, 0, 0
			backgroundLegacyFixture = func(context.Context, []string) error {
				legacyCalls++ // old-code proof must not open the person's app
				return nil
			}
			backgroundOpenFixture = func(ctx context.Context, path string, argv, env []string) ([]byte, error) {
				if path != binary {
					t.Fatal("fixture helper resolution drifted:", path)
				}
				switch {
				case reflect.DeepEqual(argv, []string{"--status"}):
					statusCalls++
				case reflect.DeepEqual(argv, []string{"--open-background", "codex://threads/focus-fixture"}):
					modeCalls++
				default:
					t.Fatal("unexpected helper argv:", argv)
				}
				cmd := exec.CommandContext(ctx, path, argv...)
				cmd.Env = append(os.Environ(), env...)
				cmd.Env = append(cmd.Env, "DIBS_BACKGROUND_OPEN_FIXTURE="+string(payload))
				raw, err := cmd.Output()
				if argv[0] == "--open-background" {
					if decodeErr := json.Unmarshal(raw, &receipt); decodeErr != nil {
						t.Fatalf("native receipt: %q %v", raw, decodeErr)
					}
				}
				return raw, err
			}
			err = OpenChatGPTBackground("codex://threads/focus-fixture")
			if statusCalls != 1 || modeCalls != 1 || legacyCalls != 0 {
				t.Fatalf("native mode missing: status=%d mode=%d legacy=%d err=%v", statusCalls, modeCalls, legacyCalls, err)
			}
			if (err == nil) != tc.open {
				t.Fatalf("open outcome: err=%v open=%v", err, tc.open)
			}
			if receipt["reason"] != tc.reason || receipt["fixture_restore_count"] != float64(tc.attempts) || receipt["fixture_open_count"] != float64(1) {
				t.Fatalf("native decision: %v; want reason=%s restores=%d opens=1", receipt, tc.reason, tc.attempts)
			}
		})
	}
}

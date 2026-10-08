// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package notify

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"testing"
)

func TestBackgroundOpenKeepsOldHelperWakeAndLogsFallbackOnce(t *testing.T) {
	previousHelper, previousOutput, previousOS := backgroundHelper, backgroundOpenFixture, goos
	t.Cleanup(func() { backgroundHelper, backgroundOpenFixture, goos = previousHelper, previousOutput, previousOS })
	previousLegacy := backgroundLegacyFixture
	t.Cleanup(func() { backgroundLegacyFixture = previousLegacy })
	backgroundFallbackNotice.Lock()
	previousLogged := backgroundFallbackNotice.logged
	backgroundFallbackNotice.logged = false
	backgroundFallbackNotice.Unlock()
	t.Cleanup(func() {
		backgroundFallbackNotice.Lock()
		backgroundFallbackNotice.logged = previousLogged
		backgroundFallbackNotice.Unlock()
	})
	var logs bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })
	goos = "darwin"
	backgroundHelper = func() string { return "fixture-helper" }
	calls := 0
	backgroundOpenFixture = func(ctx context.Context, binary string, argv, env []string) ([]byte, error) {
		calls++
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("unbounded helper contact")
		}
		if !reflect.DeepEqual(argv, []string{"--status"}) || !reflect.DeepEqual(env, []string{"DIBS_BACKGROUND_OPEN_V1=1"}) {
			t.Fatalf("unsafe old-helper contact: %q %q", argv, env)
		}
		return []byte("authorized\n"), nil
	}
	opens := 0
	backgroundLegacyFixture = func(ctx context.Context, argv []string) error {
		opens++
		if _, ok := ctx.Deadline(); !ok || ctx.Err() != nil {
			t.Fatal("legacy opener must have a fresh bounded context")
		}
		if !reflect.DeepEqual(argv, []string{"/usr/bin/open", "-g", "codex://threads/compatibility"}) {
			t.Fatalf("legacy open changed: %q", argv)
		}
		return nil
	}
	for range 3 {
		if err := OpenChatGPTBackground("codex://threads/compatibility"); err != nil {
			t.Fatal("old helper stranded wake:", err)
		}
	}
	if calls != 3 || opens != 3 {
		t.Fatalf("old helper: status=%d opens=%d", calls, opens)
	}
	if strings.Count(logs.String(), `"level":"INFO"`) != 1 || !strings.Contains(logs.String(), "matching the Dibs binaries") {
		t.Fatal("fallback diagnostic must be one INFO with corrective hint:", logs.String())
	}
}

func TestBackgroundOpenKeepsWakeWithoutHelperOrCapability(t *testing.T) {
	previousHelper, previousOutput, previousOS := backgroundHelper, backgroundOpenFixture, goos
	previousLegacy := backgroundLegacyFixture
	t.Cleanup(func() {
		backgroundHelper, backgroundOpenFixture, goos = previousHelper, previousOutput, previousOS
		backgroundLegacyFixture = previousLegacy
	})
	goos = "darwin"
	for _, name := range []string{"missing-helper", "capability-timeout"} {
		t.Run(name, func(t *testing.T) {
			backgroundHelper = func() string {
				if name == "missing-helper" {
					return ""
				}
				return "fixture-helper"
			}
			backgroundOpenFixture = func(_ context.Context, _ string, argv, _ []string) ([]byte, error) {
				if name == "missing-helper" || !reflect.DeepEqual(argv, []string{"--status"}) {
					t.Fatal("unexpected helper contact:", argv)
				}
				return nil, context.DeadlineExceeded
			}
			opens := 0
			backgroundLegacyFixture = func(ctx context.Context, _ []string) error {
				opens++
				if _, ok := ctx.Deadline(); !ok || ctx.Err() != nil {
					t.Fatal("capability failure contaminated fallback context")
				}
				return nil
			}
			if err := OpenChatGPTBackground("codex://threads/compatibility"); err != nil || opens != 1 {
				t.Fatalf("fallback: opens=%d err=%v", opens, err)
			}
		})
	}
}

func TestBackgroundOpenNeverRetriesAmbiguousNativeOutcome(t *testing.T) {
	previousHelper, previousOutput, previousOS := backgroundHelper, backgroundOpenFixture, goos
	t.Cleanup(func() { backgroundHelper, backgroundOpenFixture, goos = previousHelper, previousOutput, previousOS })
	previousLegacy := backgroundLegacyFixture
	t.Cleanup(func() { backgroundLegacyFixture = previousLegacy })
	backgroundLegacyFixture = func(context.Context, []string) error {
		t.Fatal("ambiguous open must never fall back and retry")
		return nil
	}
	goos = "darwin"
	backgroundHelper = func() string { return "fixture-helper" }
	for _, name := range []string{"bad-receipt", "lost-reply"} {
		t.Run(name, func(t *testing.T) {
			opens := 0
			backgroundOpenFixture = func(_ context.Context, _ string, argv, _ []string) ([]byte, error) {
				if reflect.DeepEqual(argv, []string{"--status"}) {
					return []byte(`{"background_open":1}`), nil
				}
				opens++
				if name == "lost-reply" {
					return nil, errors.New("reply lost after accepted open")
				}
				return []byte(`{"opened":true}`), nil
			}
			if err := OpenChatGPTBackground("codex://threads/ambiguous"); err == nil || opens != 1 {
				t.Fatalf("ambiguous open retried or confirmed: opens=%d err=%v", opens, err)
			}
		})
	}
}

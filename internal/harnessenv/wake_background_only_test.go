// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package harnessenv

import (
	"strings"
	"testing"
	"time"
)

// Enter each wake/recovery entrance and replace only the final app contact.
// argv alone with -g is insufficient: the ordinary thread URL navigates.
func TestWakeRoutesNeverNavigateOrActivate(t *testing.T) {
	const thread = "01696949-1111-4222-8333-444455556666"
	const wakeURL = "codex://threads/wake/" + thread
	for _, route := range []string{"cold", "legacy-show", "claude-show"} {
		t.Run(route, func(t *testing.T) {
			t.Setenv("DIBS_DIR", t.TempDir())
			calls := 0
			s := Shower{Holds: func(string) bool { return false }, Open: func(argv []string) error {
				calls++
				if len(argv) != 3 || argv[0] != "/usr/bin/open" || argv[1] != "-g" || argv[2] != wakeURL {
					t.Errorf("wake attempted focus/navigation argv: %q", argv)
				}
				return nil
			}, Wait: func(time.Duration) {}}
			switch route {
			case "cold":
				_, _ = s.Show(OpenArgv(ChatGPTApp, thread), thread)
			case "legacy-show":
				_, _ = s.Show(ChatGPTOpenArgv(thread), thread)
			case "claude-show":
				_, _ = s.Show(ClaudeOpenArgv("local_background-guard"), thread)
			}
			want := 1
			if route == "claude-show" {
				want = 0
			}
			if calls != want {
				t.Fatalf("%s app contacts=%d, want %d", route, calls, want)
			}
		})
	}
}

func TestRealShowerRefusesRetiredNavigation(t *testing.T) {
	previous := appOpenFixture
	calls := 0
	appOpenFixture = func(string, string, time.Duration) error { calls++; return nil }
	t.Cleanup(func() { appOpenFixture = previous })
	for _, argv := range [][]string{
		ChatGPTOpenArgv("01696949-1111-4222-8333-444455556666"),
		{"/usr/bin/open", "-g", "codex://threads/01696949-1111-4222-8333-444455556666"},
		{"/usr/bin/open", "-g", "codex://threads/new"},
		ClaudeOpenArgv("local_background-guard"),
	} {
		if err := RealShower.Open(argv); err == nil || !strings.Contains(err.Error(), "unsupported") {
			t.Errorf("retired route was not refused: %q: %v", argv, err)
		}
	}
	if calls != 0 {
		t.Fatalf("retired routes reached app contact %d times", calls)
	}
}

// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package harnessenv

import (
	"context"
	"errors"
	"testing"
	"time"
)

// The person quit ChatGPT and Dibs launched it again, through both the cold
// route and the reload pass (2026-11-11). Both reach the app through
// RealShower's opener, so the refusal is tested there, with only the final
// native contact faked.
func TestRealShowerNeverLaunchesAQuitChatGPT(t *testing.T) {
	t.Setenv("DIBS_DIR", t.TempDir())
	t.Setenv("DIBS_TEST_FORBID_APP_OPEN", "1")
	previousOpen, previousRunning := appOpenFixture, chatGPTMainRunning
	t.Cleanup(func() { appOpenFixture, chatGPTMainRunning = previousOpen, previousRunning })
	calls := 0
	appOpenFixture = func(string, string, time.Duration) error { calls++; return nil }
	chatGPTMainRunning = func() bool { return false }
	s := RealShower
	s.Holds = func(string) bool { return false }

	// The cold route.
	opened, err := s.Show(ChatGPTOpenArgv("quit-cold"), "quit-cold")
	if opened || !errors.Is(err, ErrChatGPTNotRunning) || calls != 0 {
		t.Fatalf("cold route with the app quit: opened=%v calls=%d err=%v", opened, calls, err)
	}
	// The reload pass after an app epoch change.
	notOwned := func(string) (bool, error) { return false, nil }
	_, opened2, err := s.LoadChatGPTThreads([]string{"quit-reload"}, notOwned, time.Millisecond)
	if opened2 != 0 || !errors.Is(err, ErrChatGPTNotRunning) || calls != 0 {
		t.Fatalf("reload pass with the app quit: opened=%d calls=%d err=%v", opened2, calls, err)
	}

	// The same doors still open a running app: the guard is the app's state,
	// not a route switched off.
	chatGPTMainRunning = func() bool { return true }
	if opened, err := s.Show(ChatGPTOpenArgv("running-cold"), "running-cold"); err != nil || !opened || calls != 1 {
		t.Fatalf("cold route with the app running: opened=%v calls=%d err=%v", opened, calls, err)
	}
}

// Helpers, crashpad and the bundled codex outlive a Quit. Only the main
// executable is the app.
func TestChatGPTMainRunningIgnoresBundleRemnants(t *testing.T) {
	p := &processPass{ctx: context.Background(), rows: map[int]processIdentity{
		10: {parent: 1, comm: "ChatGPT Helper", path: chatGPTRoot + "Contents/Frameworks/ChatGPT Helper.app/Contents/MacOS/ChatGPT Helper"},
		11: {parent: 1, comm: "codex", path: chatGPTRoot + "Contents/Resources/codex-cli/bin/codex"},
		12: {parent: 1, comm: "chrome_crashpad_handler", path: chatGPTRoot + "Contents/Frameworks/chrome_crashpad_handler"},
	}, paths: map[int]processPath{}}
	if p.mainAppRunning() {
		t.Fatal("bundle remnants of a quit app read as the app running")
	}
	p.rows[13] = processIdentity{parent: 1, comm: "ChatGPT", path: chatGPTRoot + "Contents/MacOS/ChatGPT"}
	if !p.mainAppRunning() {
		t.Fatal("the app's main process was not recognised")
	}
}

// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package harnessenv

import (
	"strings"
	"testing"
	"time"
)

// A thread the app already holds is not navigated to, and a pass that opened
// nothing leaves the window where the person put it: the return to the launch
// view is only ever the end of a pass that moved the window.
func TestLoadingThreadsOpensOnlyUnloadedOnesAndReturnsHomeOnlyAfterAnOpen(t *testing.T) {
	t.Setenv("DIBS_DIR", t.TempDir())
	previousCache := backgroundPairCacheDir
	backgroundPairCacheDir = func() (string, error) { return t.TempDir(), nil }
	t.Cleanup(func() { backgroundPairCacheDir = previousCache })
	const held, cold = "11111111-2222-4333-8444-555566667777", "99999999-2222-4333-8444-555566667777"
	loaded := map[string]bool{held: true}
	var opens []string
	s := Shower{
		Open: func(argv []string) error {
			url := argv[len(argv)-1]
			opens = append(opens, url)
			loaded[strings.TrimPrefix(url, "codex://threads/")] = true
			return nil
		},
		Wait: func(time.Duration) {},
	}
	owned := func(thread string) (bool, error) { return loaded[thread], nil }

	got, opened, err := s.LoadChatGPTThreads([]string{held, cold, "not a thread"}, owned, time.Second)
	if err != nil || got != 2 || opened != 1 ||
		strings.Join(opens, " ") != "codex://threads/"+cold+" codex://threads/new" {
		t.Fatalf("loaded=%d opened=%d err=%v opens=%q", got, opened, err, opens)
	}
	opens = nil
	if _, opened, err = s.LoadChatGPTThreads([]string{held, cold}, owned, time.Second); err != nil || opened != 0 || len(opens) != 0 {
		t.Fatalf("a pass with everything loaded moved the window: opened=%d err=%v opens=%q", opened, err, opens)
	}
}

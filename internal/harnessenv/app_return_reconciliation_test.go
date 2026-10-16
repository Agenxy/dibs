// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package harnessenv

import (
	"testing"
	"time"
)

// The 2026-10-15 restart pass reported 7/14 at 16:33:02Z, although the app
// logged successful asynchronous resumes through 16:33:01Z. Navigation being
// accepted does not make ownership synchronous. Drive the production loader:
// the first thread becomes owned while the second navigation is admitted.
func TestLoadingThreadsCountsLateOwnershipAtEndOfPass(t *testing.T) {
	t.Setenv("DIBS_DIR", t.TempDir())
	const first = "11111111-2222-4333-8444-555566667777"
	const second = "99999999-2222-4333-8444-555566667777"
	loaded := map[string]bool{}
	var opens []string
	s := Shower{Open: func(argv []string) error {
		url := argv[len(argv)-1]
		opens = append(opens, url)
		if url == "codex://threads/"+second {
			loaded[first], loaded[second] = true, true
		}
		return nil
	}}
	owned := func(thread string) (bool, error) { return loaded[thread], nil }
	got, opened, err := s.LoadChatGPTThreads([]string{first, second}, owned, 0)
	if !loaded[first] || !loaded[second] || opened != 2 || len(opens) != 3 {
		t.Fatalf("setup: both navigations and home must succeed and both threads become owned: loaded=%v opened=%d opens=%q", loaded, opened, opens)
	}
	if err != nil || got != 2 {
		t.Fatalf("end-of-pass ownership is 2/2; loader reported loaded=%d err=%v", got, err)
	}
}

// An accepted open with no owner is a partial recovery, never a successful
// complete load. No clock delay is needed to expose that distinction.
func TestLoadingThreadsDoesNotReportSuccessWhileAThreadRemainsUnowned(t *testing.T) {
	t.Setenv("DIBS_DIR", t.TempDir())
	const thread = "11111111-2222-4333-8444-555566667777"
	var opens int
	s := Shower{Open: func([]string) error { opens++; return nil }, Wait: func(time.Duration) {}}
	loaded, opened, err := s.LoadChatGPTThreads([]string{thread}, func(string) (bool, error) { return false, nil }, 0)
	if loaded != 0 || opened != 1 || opens != 2 {
		t.Fatalf("setup: one navigation and home must succeed without an owner: loaded=%d opened=%d opens=%d", loaded, opened, opens)
	}
	if err == nil {
		t.Fatal("unowned thread was reported as a successful complete load")
	}
}

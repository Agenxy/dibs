// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package harnessenv

import (
	"errors"
	"testing"
)

func TestUnknownRestartOwnershipNeverQuarantinesAThread(t *testing.T) {
	t.Setenv("DIBS_DIR", t.TempDir())
	const thread = "11111111-2222-4333-8444-555566667777"
	opened := 0
	s := Shower{Open: func(argv []string) error {
		if argv[len(argv)-1] != "codex://threads/new" {
			opened++
		}
		return nil
	}}
	if _, _, err := s.LoadChatGPTThreads([]string{thread}, func(string) (bool, error) { return false, errors.New("fixture owner observation unavailable") }, 0); err == nil {
		t.Fatal("unknown ownership reported complete")
	}
	// With no intervening ownership recovery, its next unloaded state must
	// remain eligible: unknown alone never establishes a stale binding.
	_, _, _ = s.LoadChatGPTThreads([]string{thread}, func(string) (bool, error) { return false, nil }, 0)
	if opened != 2 {
		t.Fatalf("unknown observation suppressed a later open: %d", opened)
	}
}

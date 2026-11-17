// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package harnessenv

import (
	"testing"
	"testing/synctest"
	"time"
)

func TestUnknownOwnershipNeverQuarantinesAnOrdinaryWake(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		t.Setenv("DIBS_DIR", t.TempDir())
		const thread = "11111111-2222-4333-8444-555566667777"
		opened := 0
		s := Shower{Ownership: func(string) ThreadOwnership { return ThreadOwnership{Running: true, Known: false, Epoch: "app:A"} }, Open: func([]string) error { opened++; return nil }}
		for i := 0; i < 2; i++ {
			if i > 0 {
				synctest.Sleep(AppOpenRetryAfter + time.Second)
			}
			if did, err := s.Show(OpenArgv(ChatGPTApp, thread), thread); err != nil || !did {
				t.Fatalf("eligible request refused: %v %v", did, err)
			}
		}
		stale, err := ChatGPTBindingStale(thread)
		if opened != 2 || stale || err != nil {
			t.Fatalf("unknown ownership suppressed later work: opens=%d stale=%v err=%v", opened, stale, err)
		}
	})
}

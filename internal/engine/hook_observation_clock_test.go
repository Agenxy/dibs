// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

func TestHookObservationUsesTheConstructorsClock(t *testing.T) {
	// No Run, listener, subprocess or watcher in this bubble. The real
	// constructor and reader use time directly; no age flag is supplied.
	synctest.Test(t, func(t *testing.T) {
		e := New(core.NewState("hook-clock", core.DefaultLimits()), &memLedger{}, nil)
		if e.hooks.startedAt.IsZero() || !e.hooks.startedAt.Equal(time.Now()) {
			t.Fatal("constructor did not initialize its real observation clock")
		}
		if h := e.HookHealth(); h.Verdict != "observing" || h.ObservedSeconds != 0 {
			t.Fatalf("fresh constructor: %+v", h)
		}
		synctest.Sleep(time.Minute - time.Nanosecond)
		if h := e.HookHealth(); h.Verdict != "observing" || h.ObservedSeconds != 59 {
			t.Fatalf("before the full observation window: %+v", h)
		}
		synctest.Sleep(time.Nanosecond)
		if h := e.HookHealth(); h.Verdict != "never-called" || h.ObservedSeconds != 60 || !strings.Contains(h.Hint, "Install the plugin") {
			t.Fatalf("expired observation window: %+v", h)
		}
		synctest.Sleep(time.Second)
		if h := e.HookHealth(); h.ObservedSeconds != 61 {
			t.Fatalf("elapsed observation lost the real clock: %+v", h)
		}
	})
}

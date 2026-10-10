// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package harnessenv

import (
	"errors"
	"sync"
	"time"
)

// pendingOpens coalesces only bounded ChatGPT open/restore lock contention.
var pendingOpens = struct {
	sync.Mutex
	threads map[string]bool
}{threads: map[string]bool{}}

// PendingAppOpen reports a local, derived waiter. Losing it leaves the notice
// in the app queue; this is a display diagnostic, never coordination truth.
func PendingAppOpen(thread string) bool {
	pendingOpens.Lock()
	defer pendingOpens.Unlock()
	return pendingOpens.threads[thread]
}

// ShowWhenIdle is the retained call name for immediate recovery. It reads no
// presence or idle state. ChatGPT pair-lock contention alone may defer briefly.
func (s Shower) ShowWhenIdle(argv []string, thread string, report func(opened, deferred bool, err error)) {
	if isChatGPTOpen(argv) {
		s.showChatGPTWhenIdle(argv, thread, report)
		return
	}
	opened, err := s.Show(argv, thread)
	if opened || err != nil {
		report(opened, false, err)
	}
}

func (s Shower) showChatGPTWhenIdle(argv []string, thread string, report func(bool, bool, error)) {
	opened, err := s.showChatGPT(argv, thread)
	if errors.Is(err, ErrAppOpenPairBusy) {
		s.deferChatGPTPair(argv, thread, report)
		return
	}
	report(opened, false, err)
}

// The app's global open/restore pair can be held by another thread or process.
// Wait only on this bounded delivery worker, never on the engine writer. The
// per-thread map coalesces repeated wakes while the attempt memo stays untouched.
func (s Shower) deferChatGPTPair(argv []string, thread string, report func(bool, bool, error)) {
	pendingOpens.Lock()
	if pendingOpens.threads[thread] {
		pendingOpens.Unlock()
		return
	}
	pendingOpens.threads[thread] = true
	pendingOpens.Unlock()
	report(false, true, nil)
	go func() {
		defer func() {
			pendingOpens.Lock()
			delete(pendingOpens.threads, thread)
			pendingOpens.Unlock()
		}()
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if s.Wait != nil {
				s.Wait(25 * time.Millisecond)
			} else {
				<-time.After(25 * time.Millisecond)
			}
			opened, err := s.showChatGPT(argv, thread)
			if errors.Is(err, ErrAppOpenPairBusy) {
				continue
			}
			report(opened, false, err)
			return
		}
		report(false, false, ErrAppOpenPairBusy)
	}()
}

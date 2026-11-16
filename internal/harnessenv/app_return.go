// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package harnessenv

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/agenxy/dibs/internal/paths"
)

// ChatGPTThreadOwnershipError records the final discovery results that did
// not confirm ownership. Unknown is distinct from the app saying no owner.
type ChatGPTThreadOwnershipError struct {
	Unowned, Unknown []string
	Total            int
}

func (e *ChatGPTThreadOwnershipError) Error() string {
	return fmt.Sprintf("end-of-pass ownership confirmed for %d of %d threads",
		e.Total-len(e.Unowned)-len(e.Unknown), e.Total)
}

// LoadChatGPTThreads offers pending work to a restarted app's wake-only
// handler, without selecting a chat or returning its window to a launch view.
// It retains the desktop-wide lock for compatibility with older producers.
// owned is the app's owner discovery: already-owned threads need no URL.
// opened counts URL requests; loaded counts ownership confirmed at the END
// of the pass. The app may resume asynchronously while later requests run.
// An incomplete final measurement returns an error, never a complete load.
func (s Shower) LoadChatGPTThreads(
	threads []string, owned func(string) (bool, error), settle time.Duration,
) (loaded, opened int, err error) {
	attempted := map[string]bool{}
	dir := filepath.Join(paths.DataDir(), "app-opens")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return 0, 0, err
	}
	pair, err := s.waitBackgroundPair(dir)
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = pair.Close() }()
	defer paths.Unlock(pair)
	defer func() {
		// Reconcile even on an open failure: earlier navigations may have
		// completed meanwhile. Do not reopen anything or extend its wait.
		var measurementErr error
		loaded, measurementErr = reconcileChatGPTThreads(threads, owned, attempted)
		err = errors.Join(err, measurementErr)
	}()
	for _, thread := range threads {
		didOpen, openErr := s.loadChatGPTThread(thread, owned, settle)
		if openErr != nil {
			return loaded, opened, openErr
		}
		if didOpen {
			opened++
			attempted[thread] = true
		}
	}
	return loaded, opened, nil
}

func (s Shower) loadChatGPTThread(thread string, owned func(string) (bool, error), settle time.Duration) (bool, error) {
	argv := chatGPTWakeArgv(thread)
	if argv == nil {
		return false, nil
	}
	if held, err := owned(thread); err == nil && held {
		return false, rememberChatGPTBinding(thread, false)
	}
	stale, err := ChatGPTBindingStale(thread)
	if err != nil {
		return false, err
	}
	if stale || !s.allowsOpen(thread) {
		return false, nil
	}
	if err = s.Open(argv); err != nil {
		return false, err
	}
	s.waitOwned(thread, owned, settle)
	return true, nil
}

func reconcileChatGPTThreads(
	threads []string, owned func(string) (bool, error), attempted map[string]bool,
) (int, error) {
	loaded, err := measureChatGPTThreads(threads, owned)
	var missing *ChatGPTThreadOwnershipError
	if errors.As(err, &missing) {
		for _, thread := range missing.Unowned {
			if attempted[thread] && codexThreadMissing(thread) {
				err = errors.Join(err, rememberChatGPTBinding(thread, true))
			}
		}
	}
	return loaded, err
}

func measureChatGPTThreads(threads []string, owned func(string) (bool, error)) (loaded int, err error) {
	missing := &ChatGPTThreadOwnershipError{}
	for _, thread := range threads {
		if chatGPTWakeArgv(thread) == nil {
			continue
		}
		missing.Total++
		held, probeErr := owned(thread)
		switch {
		case probeErr != nil:
			missing.Unknown = append(missing.Unknown, thread)
			if err == nil {
				err = fmt.Errorf("end-of-pass owner discovery: %w", probeErr)
			}
		case held:
			loaded++
		default:
			missing.Unowned = append(missing.Unowned, thread)
		}
	}
	if loaded != missing.Total {
		err = errors.Join(err, missing)
	}
	return loaded, err
}

func (s Shower) waitBackgroundPair(dir string) (*os.File, error) {
	deadline := time.Now().Add(10 * time.Second)
	for {
		pair, err := lockBackgroundPair(dir)
		if !errors.Is(err, ErrAppOpenPairBusy) || !time.Now().Before(deadline) {
			return pair, err
		}
		s.pause(50 * time.Millisecond)
	}
}

func (s Shower) waitOwned(thread string, owned func(string) (bool, error), settle time.Duration) bool {
	deadline := time.Now().Add(settle)
	for {
		if held, err := owned(thread); err == nil && held {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		s.pause(250 * time.Millisecond)
	}
}

func (s Shower) pause(d time.Duration) {
	if s.Wait != nil {
		s.Wait(d)
		return
	}
	<-time.After(d)
}

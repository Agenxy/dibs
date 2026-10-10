// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package harnessenv

import (
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/agenxy/dibs/internal/paths"
)

// chatGPTHomeURL returns the app's primary window to its launch view, "/".
// Read from the deep link handler of ChatGPT 26.1002.52244: threads/new with
// no path, origin or project resolves no workspace, opens no dialog, and
// navigates the primary window to "/", which is also the route the window
// starts on after a launch.
const chatGPTHomeURL = "codex://threads/new"

// LoadChatGPTThreads loads every thread into a ChatGPT app that has just come
// back, in one pass, and leaves the window where a launch leaves it.
//
// The app loads a thread into its runtime only by navigating its window to
// it, and after a restart it has loaded none. Before this, each agent's next
// mail opened that agent's thread when it arrived, so the person's window
// moved once per agent, at moments nobody chose, for as long as mail kept
// arriving. Loading them all straight after the restart concentrates that
// into the seconds when the window is still on its launch view, and returns
// it there; after that every delivery is native and nothing navigates.
//
// It holds the desktop-wide open/restore lock for the whole pass, so no
// per-mail open interleaves with it. owned is the app's own owner discovery:
// a thread already loaded is not opened, and an opened one is waited on, up
// to settle, before the next. opened counts the opens made; loaded counts the
// threads the app reported holding afterwards.
func (s Shower) LoadChatGPTThreads(
	threads []string, owned func(string) (bool, error), settle time.Duration,
) (loaded, opened int, err error) {
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
	for _, thread := range threads {
		argv := chatGPTWakeArgv(thread)
		if argv == nil {
			continue
		}
		if held, err := owned(thread); err == nil && held {
			loaded++
			continue
		}
		if err := s.Open(argv); err != nil {
			return loaded, opened, err
		}
		opened++
		if s.waitOwned(thread, owned, settle) {
			loaded++
		}
	}
	if opened > 0 {
		if err := s.Open([]string{"/usr/bin/open", "-g", chatGPTHomeURL}); err != nil {
			return loaded, opened, err
		}
	}
	return loaded, opened, nil
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

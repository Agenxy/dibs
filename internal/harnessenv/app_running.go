// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package harnessenv

import (
	"context"
	"errors"
	"time"
)

// ErrChatGPTNotRunning is an open Dibs declined because opening a thread in an
// app that is not running launches it. Quitting the app is the person's
// decision: the mail stays in the mailbox and the person is asked instead.
var ErrChatGPTNotRunning = errors.New("the ChatGPT app is not running, and Dibs never launches it; " +
	"the thread was not opened, and mail waits until the person opens ChatGPT")

// chatGPTMainRunning gates every ChatGPT thread open in RealShower. Tests
// replace it; production asks the process table at the moment of the open.
var chatGPTMainRunning = ChatGPTMainRunning

// ChatGPTMainRunning reports whether the app's own main process is running.
// Helpers, crashpad and the bundled codex outlive a Quit for a while, so any
// process in the bundle is not evidence: on 2026-11-11 a quit in progress read
// as a restart, and the reload pass relaunched the app under the person.
// Unreadable evidence is not running, because the cost of guessing wrong is
// launching an app the person closed.
func ChatGPTMainRunning() bool {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	processes, err := appProcessPass(ctx)
	if err != nil {
		return false
	}
	return processes.mainAppRunning()
}

func (p *processPass) mainAppRunning() bool {
	for pid, row := range p.rows {
		if !appProcessCandidate(row) {
			continue
		}
		if path := p.path(pid); path.state == processPresent && path.value == chatGPTRoot+"Contents/MacOS/ChatGPT" {
			return true
		}
	}
	return false
}

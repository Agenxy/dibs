// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"encoding/json"
	"os"
	"sync"
	"time"

	"github.com/agenxy/dibs/internal/harnessenv"
	"github.com/agenxy/dibs/internal/mcp"
)

var bridgeStart struct {
	sync.Mutex
	at      string
	pending chan struct{} // the one lookup in flight, if any
}

// lookupSelfStart reads this process's start stamp from the kernel. A var
// so a test can make it hang.
var lookupSelfStart = func() string { return harnessenv.ProcessStart(os.Getpid()) }

// bridgeStartBudget is the most a request waits for the stamp.
//
// THE STAMP IS ADDITIVE AND THE REQUEST IS NOT. It was looked up inline on
// the first request, and on the hosted macOS gate that kernel call (its
// sysctl name-to-MIB step) twice took past the five-second bound with the
// request loop parked inside it, so the agent's first register got no
// reply (#482). It now starts with the bridge, in the background, and a
// request goes without it after this long; a later request carries it.
const bridgeStartBudget = 500 * time.Millisecond

// primeBridgeStart starts the lookup if none is done or in flight, and
// returns what to wait on.
func primeBridgeStart() <-chan struct{} {
	bridgeStart.Lock()
	defer bridgeStart.Unlock()
	if bridgeStart.at != "" {
		done := make(chan struct{})
		close(done)
		return done
	}
	if bridgeStart.pending == nil {
		ch := make(chan struct{})
		bridgeStart.pending = ch
		go func() {
			at := lookupSelfStart()
			bridgeStart.Lock()
			if bridgeStart.at == "" {
				bridgeStart.at = at
			}
			bridgeStart.pending = nil // a failed lookup is retried by the next request
			bridgeStart.Unlock()
			close(ch)
		}()
	}
	return bridgeStart.pending
}

func bridgeStarted() string {
	wait := time.NewTimer(bridgeStartBudget)
	defer wait.Stop()
	select {
	case <-primeBridgeStart():
	case <-wait.C:
	}
	bridgeStart.Lock()
	defer bridgeStart.Unlock()
	return bridgeStart.at
}

// Startup discovery precedes the first model call and carries no agent token.
// This additive observation identifies an app reconnect without guessing which
// agent a new bridge serves. Per-request metadata also works without initialize.
func enrichBridgeProcess(line []byte) []byte {
	if remoteSession {
		return line
	}
	var msg map[string]any
	if json.Unmarshal(line, &msg) != nil {
		return line
	}
	if _, request := msg["method"]; !request {
		return line // a response to a server request has no params member
	}
	started := bridgeStarted()
	if started == "" {
		return line
	}
	params, _ := msg["params"].(map[string]any)
	if params == nil {
		params = map[string]any{}
		msg["params"] = params
	}
	meta, _ := params["_meta"].(map[string]any)
	if meta == nil {
		meta = map[string]any{}
		params["_meta"] = meta
	}
	meta[mcp.BridgePIDMetaKey] = os.Getpid()
	meta[mcp.BridgeStartMetaKey] = started
	// A tunnel's host must be stated on discovery too: loopback alone does
	// not establish that the PID is on the daemon's computer.
	if hid := hostID(); hid != "" {
		meta[mcp.HostMetaKey] = hid
	}
	if encoded, err := json.Marshal(msg); err == nil {
		return encoded
	}
	return line
}

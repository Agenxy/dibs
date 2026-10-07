// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"encoding/json"
	"os"
	"sync"

	"github.com/agenxy/dibs/internal/harnessenv"
	"github.com/agenxy/dibs/internal/mcp"
)

var bridgeStart struct {
	sync.Mutex
	at string
}

func bridgeStarted() string {
	bridgeStart.Lock()
	defer bridgeStart.Unlock()
	if bridgeStart.at == "" {
		bridgeStart.at = harnessenv.ProcessStart(os.Getpid())
	}
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

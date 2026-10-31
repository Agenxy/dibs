// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
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
// request goes without it after this long; its completion delivers the stamp
// even when the model makes no later request.
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
	return enrichBridgeProcessWithCompletion(line, nil)
}

func enrichBridgeProcessWithCompletion(line []byte, omitted chan<- struct{}) []byte {
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
		select {
		case omitted <- struct{}{}:
		default:
		}
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
	meta[mcp.BridgeToolsHashMetaKey], meta[mcp.BridgeToolsRevisionMetaKey] = catalogServed()
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

// One session-owned listener, armed by an omitted stamp or a served catalog.
// This metadata-only notification is additive: an older daemon ignores its
// unknown method, while an older bridge still uses ordinary request metadata.
// It names no agent or session and cannot bind either. The daemon verifies
// the process incarnation and actual ancestry independently.
func deliverBridgeStart(ctx context.Context, client *http.Client, url, secret string, omitted <-chan struct{}) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-omitted:
		}
		started := completedBridgeStart(ctx)
		if started == "" {
			if ctx.Err() == nil {
				slog.Warn("bridge start stamp unavailable after completion retry; a later request may retry")
			}
			continue
		}
		line := enrichBridgeProcess([]byte(`{"jsonrpc":"2.0","method":"notifications/dibs/bridge-started"}`))
		if err := postBridgeStart(ctx, client, url, secret, line); err != nil && ctx.Err() == nil {
			slog.Warn("bridge start completion was not delivered; a later request retries its metadata", "err", err)
		}
	}
}

// A failure is an event too. Consume at most two results for this omitted
// request, sharing any in-flight lookup. A second failure leaves the stamp
// unknown until a later real request; there is no timer or retry loop.
func completedBridgeStart(ctx context.Context) string {
	for range 2 {
		if ctx.Err() != nil {
			return ""
		}
		select {
		case <-ctx.Done():
			return ""
		case <-primeBridgeStart():
		}
		bridgeStart.Lock()
		started := bridgeStart.at
		bridgeStart.Unlock()
		if started != "" {
			return started
		}
	}
	return ""
}

func postBridgeStart(ctx context.Context, client *http.Client, url, secret string, line []byte) error {
	ctx, cancel := context.WithTimeout(ctx, startupGrace)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(line))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("MCP-Protocol-Version", "2026-07-28")
	req.Header.Set("X-Dibs-Local", secret) // refreshed by the normal guarded transport
	resp, err := doWithRestartGrace(client, req, line)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	_, err = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if err == nil && resp.StatusCode != http.StatusAccepted {
		err = fmt.Errorf("completion notification returned HTTP %d", resp.StatusCode)
	}
	return err
}

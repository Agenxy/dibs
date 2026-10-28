// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"sync"

	"github.com/agenxy/dibs/internal/harnessenv"
	"github.com/agenxy/dibs/internal/mcp"
)

const maxSurfaceRepairs = 512

type surfaceRepair struct {
	token string
	codex bool // fallback only after a successful inventory found no app
}

type surfaceCompletion struct {
	mu      sync.Mutex
	pending map[string]surfaceRepair
	changed chan struct{}
	probe   *surfaceProbe
	// A negative reply is final for this address selection. Only the normal
	// bridge re-resolution permits another attempt, never a timer or fallback.
	unsupported bool
	board       surfaceBoard
}

type surfaceBoard struct {
	dir, origin string
	generation  uint64
}

func surfaceBoardFor(client *http.Client, url string) surfaceBoard {
	if t, ok := client.Transport.(*bridgeDirectoryTransport); ok {
		_, _, _ = t.route.current(false)
		t.route.mu.Lock()
		defer t.route.mu.Unlock()
		return surfaceBoard{t.route.dir, t.route.origin, t.route.generation}
	}
	return surfaceBoard{origin: url}
}

// One session-owned worker, never one goroutine per registration. Its only
// wakeups are completed real RPC replies; late discovery itself is awaited
// through the probe's completion channel. No model request waits for it.
func surfaceOnRegister(
	ctx context.Context, client *http.Client, url, secret string, streams *sync.WaitGroup,
	next func([]byte, []byte),
) func([]byte, []byte) {
	if remoteSession {
		return next
	}
	c := &surfaceCompletion{pending: map[string]surfaceRepair{}, changed: make(chan struct{}, 1), probe: bridgeAncestry}
	streams.Add(1)
	go func() {
		defer streams.Done()
		c.run(ctx, client, url, secret)
	}()
	return func(sent, reply []byte) {
		next(sent, reply)
		c.remember(sent, reply)
		select {
		case c.changed <- struct{}{}:
		default:
		}
	}
}

func (c *surfaceCompletion) remember(sent, reply []byte) {
	if name := toolNameOf(sent); name != "register" && name != "resume" {
		return
	}
	var request struct {
		Params struct {
			Meta map[string]any `json:"_meta"`
		} `json:"params"`
	}
	var response struct {
		Result struct {
			Meta map[string]any `json:"_meta"`
		} `json:"result"`
	}
	if json.Unmarshal(sent, &request) != nil || json.Unmarshal(reply, &response) != nil ||
		response.Result.Meta[mcp.BridgeSurfaceEvidenceMetaKey] != true || request.Params.Meta[mcp.SurfaceMetaKey] != nil {
		return
	}
	id, token := agentIDIn(reply), agentTokenIn(reply)
	if id == "" || token == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.pending[id]; !exists && len(c.pending) >= maxSurfaceRepairs {
		slog.Warn("late bridge surface repair was omitted: pending registration bound reached")
		return
	}
	c.pending[id] = surfaceRepair{token: token, codex: clientIs("codex")}
}

func (c *surfaceCompletion) run(ctx context.Context, client *http.Client, url, secret string) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.changed:
		}
		c.advance(ctx, client, url, secret)
	}
}

func (c *surfaceCompletion) advance(ctx context.Context, client *http.Client, url, secret string) {
	board := surfaceBoardFor(client, url)
	if c.unsupported && c.board == board {
		return
	}
	c.mu.Lock()
	havePending := len(c.pending) > 0
	c.mu.Unlock()
	if !havePending {
		return
	}
	select {
	case <-ctx.Done():
		return
	case <-c.probe.prime():
	}
	surface, known := c.probe.result()
	if !known {
		return // an actual later request may retry failed discovery
	}
	c.flush(ctx, client, url, secret, surface)
}

func (c *surfaceCompletion) flush(ctx context.Context, client *http.Client, url, secret, surface string) {
	c.mu.Lock()
	batch := make(map[string]surfaceRepair, len(c.pending))
	for id, entry := range c.pending {
		batch[id] = entry
	}
	c.mu.Unlock()
	for id, entry := range batch {
		observed := surface
		if observed == "" && entry.codex {
			observed = harnessenv.CodexOutsideApp
		}
		if observed != "" {
			if err := postBridgeSurface(ctx, client, url, secret, entry.token, observed); err != nil {
				c.board, c.unsupported = surfaceBoardFor(client, url), true
				if ctx.Err() == nil {
					slog.Warn("late bridge surface repair unsuccessful; omitted until address re-resolution", "err", err)
				}
				return
			}
		}
		c.mu.Lock()
		if c.pending[id] == entry { // a rotated credential belongs to its newer reply
			delete(c.pending, id)
		}
		c.mu.Unlock()
	}
}

func postBridgeSurface(ctx context.Context, client *http.Client, url, secret, token, surface string) error {
	ctx, cancel := context.WithTimeout(ctx, startupGrace)
	defer cancel()
	line, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": "dibs-surface-evidence", "method": mcp.BridgeSurfaceEvidenceMethod,
		"params": map[string]any{"token": token, "_meta": map[string]any{
			mcp.SurfaceMetaKey: surface, mcp.HostMetaKey: hostID(),
		}},
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(line))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("MCP-Protocol-Version", "2026-07-28")
	req.Header.Set("X-Dibs-Local", secret)
	resp, err := doWithRestartGrace(client, req, line)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	var reply struct {
		Error  json.RawMessage `json:"error"`
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 65536)).Decode(&reply) != nil || resp.StatusCode != http.StatusOK ||
		len(reply.Error) > 0 || reply.Result.IsError || len(reply.Result.Content) != 1 {
		return errors.New("surface evidence RPC was not accepted")
	}
	var result struct {
		OK bool `json:"ok"`
	}
	if json.Unmarshal([]byte(reply.Result.Content[0].Text), &result) != nil || !result.OK {
		return errors.New("surface evidence RPC did not confirm the update")
	}
	return nil
}

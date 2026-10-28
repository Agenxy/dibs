// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"context"
	"encoding/json"
	"testing"
)

func TestFailedAncestryIsUnknownAndRetriesOnlyOnARequest(t *testing.T) {
	prevClient, prevDetect, prevProbe := lastClientInfo, detectedSurface, bridgeAncestry
	t.Cleanup(func() { lastClientInfo, detectedSurface, bridgeAncestry = prevClient, prevDetect, prevProbe })
	lastClientInfo = map[string]any{"name": "codex-mcp-client"}
	bridgeAncestry = &surfaceProbe{}
	calls := 0
	detectedSurface = func(context.Context) (string, error) {
		calls++
		return "", context.DeadlineExceeded
	}
	if got := bridgeSurface(); got != "" {
		t.Fatalf("failed inventory became routing evidence %q", got)
	}
	if calls != 1 {
		t.Fatalf("failed probe automatically retried: %d", calls)
	}
	if got := bridgeSurface(); got != "" || calls != 2 {
		t.Fatalf("next request did not retry unknown evidence: %q calls=%d", got, calls)
	}
}

func TestRemoteBridgeNeverProbesItsLocalAncestry(t *testing.T) {
	prevRemote, prevDetect, prevProbe := remoteSession, detectedSurface, bridgeAncestry
	t.Cleanup(func() { remoteSession, detectedSurface, bridgeAncestry = prevRemote, prevDetect, prevProbe })
	remoteSession = true
	bridgeAncestry = &surfaceProbe{}
	called := make(chan struct{}, 1)
	detectedSurface = func(context.Context) (string, error) {
		called <- struct{}{}
		return "chatgpt-app", nil
	}
	line := enrichRegister([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"check_in","arguments":{}}}`))
	select {
	case <-called:
		t.Fatal("remote conversation probed the tunnel's ancestry")
	default:
	}
	var msg struct {
		Params struct {
			Meta map[string]any `json:"_meta"`
		} `json:"params"`
	}
	if err := json.Unmarshal(line, &msg); err != nil || msg.Params.Meta["com.dibs/surface"] != nil {
		t.Fatalf("remote caller acquired the tunnel's app: %s %v", line, err)
	}
}

func TestAncestryProbeEndsWithTheBridgeSession(t *testing.T) {
	prev := detectedSurface
	t.Cleanup(func() { detectedSurface = prev })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan struct{})
	detectedSurface = func(ctx context.Context) (string, error) {
		close(entered)
		<-ctx.Done()
		return "", ctx.Err()
	}
	p := &surfaceProbe{ctx: ctx}
	done := p.prime()
	<-entered
	cancel()
	<-done
	if surface, known := p.result(); known || surface != "" {
		t.Fatalf("canceled probe became completed evidence: %q %v", surface, known)
	}
}

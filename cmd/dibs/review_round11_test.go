package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"testing"
)

// An in-place bridge upgrade carries the self-wake token and restarts the
// watcher with it, or the upgraded bridge answers every call and never wakes
// its session again.
func TestAnUpgradedBridgeKeepsItsSelfWake(t *testing.T) {
	t.Setenv("CLAUDE_CODE_MESSAGING_SOCKET", "/nonexistent/but/present.sock")
	t.Setenv("CLAUDE_CODE_MESSAGING_TOKEN", "child-token")
	t.Cleanup(func() { recordWakeToken("") })
	// The watcher records the token it subscribes with, and the handoff
	// carries whatever it recorded.
	ctx0, cancel0 := context.WithCancel(context.Background())
	defer cancel0()
	var started inboxWatcher
	started.start(ctx0, &http.Client{}, "http://127.0.0.1:1/mcp", "secret", "carried-token")
	if handoffState().WakeToken != "carried-token" {
		t.Fatalf("the handoff carries %q, not the token the watcher subscribes with", handoffState().WakeToken)
	}
	// And the cursor: the replacement must not subscribe from the present.
	started.noteSerial(map[string]any{"com.dibs/serial": float64(9)})
	if handoffState().WakeSince != 9 {
		t.Fatalf("the handoff carries cursor %d, want 9: mail arriving during the upgrade wakes nobody", handoffState().WakeSince)
	}
	env, err := carryEnv(handoffState())
	if err != nil {
		t.Fatal(err)
	}
	var blob string
	for _, kv := range env {
		if len(kv) > len(bridgeStateEnv)+1 && kv[:len(bridgeStateEnv)+1] == bridgeStateEnv+"=" {
			blob = kv[len(bridgeStateEnv)+1:]
		}
	}
	var carried bridgeState
	if err := json.Unmarshal([]byte(blob), &carried); err != nil || carried.WakeToken != "carried-token" {
		t.Fatalf("the self-wake token is not in the handoff: %+v %v", carried, err)
	}
	t.Setenv(bridgeStateEnv, blob)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var iw inboxWatcher
	var streams sync.WaitGroup
	out := &syncWriter{w: bufio.NewWriter(io.Discard)}
	restoreCarried(ctx, &http.Client{}, "http://127.0.0.1:1/mcp", "secret", out, &streams, &iw, true)
	iw.mu.Lock()
	got, since := iw.token, iw.since
	iw.mu.Unlock()
	if since != 9 {
		t.Errorf("the restored watcher holds cursor %d, want 9", since)
	}
	if got != "carried-token" {
		t.Fatalf("after the handoff the watcher holds %q: self-wake stays off until the agent "+
			"happens to register or resume again", got)
	}
}

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/agenxy/dibs/internal/mcp"
)

// A session keeps the tool list it started with, so after an in-place upgrade
// the bridge tells its harness the list changed. Measured cost of not doing
// so: a Codex worker whose session predated `waiting` wrote it into its
// declaration's text, which marks nothing, and was continued in a loop.
// Entered through the real handoff and restore.
func TestAnUpgradedBridgeTellsItsHarnessTheToolListChanged(t *testing.T) {
	restore := func(t *testing.T, carried bridgeState) string {
		t.Helper()
		blob, err := json.Marshal(carried)
		if err != nil {
			t.Fatal(err)
		}
		t.Setenv(bridgeStateEnv, string(blob))
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var buf bytes.Buffer
		out := &syncWriter{w: bufio.NewWriter(&buf)}
		var iw inboxWatcher
		var streams sync.WaitGroup
		restoreCarried(ctx, &http.Client{}, "http://127.0.0.1:1/mcp", "secret", out, &streams, &iw, true, shipTiming{})
		out.flush()
		return buf.String()
	}
	if h := handoffState().ToolsHash; h != mcp.ToolsFingerprint() {
		t.Fatalf("the handoff carries tools hash %q, want this image's %q", h, mcp.ToolsFingerprint())
	}
	if got := restore(t, bridgeState{ToolsHash: "an-older-build"}); !strings.Contains(got, "notifications/tools/list_changed") {
		t.Errorf("an upgrade that changed the tools told the harness nothing: %q", got)
	}
	if got := restore(t, bridgeState{}); !strings.Contains(got, "notifications/tools/list_changed") {
		t.Errorf("an upgrade from an image too old to carry a fingerprint told the harness nothing: %q", got)
	}
	if got := restore(t, bridgeState{ToolsHash: mcp.ToolsFingerprint()}); strings.Contains(got, "list_changed") {
		t.Errorf("an upgrade with the same tools sent a needless refresh: %q", got)
	}
}

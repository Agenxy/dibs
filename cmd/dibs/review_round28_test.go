package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"
)

// [wake] sockets = false holds across an in-place upgrade. The restore used
// to re-arm the carried inbox watcher and deliver the notice the old image
// owed with no look at the switch, so an operator who turned the route off
// and then upgraded a running bridge got a replacement that kept waking its
// session, against what the guide promises for that setting.
func TestSocketsOffHoldsAcrossAnInPlaceUpgrade(t *testing.T) {
	sock := sockPath(t)
	t.Setenv("CLAUDE_CODE_MESSAGING_SOCKET", sock)
	t.Setenv("CLAUDE_CODE_MESSAGING_TOKEN", "child-token")
	t.Cleanup(func() { recordWakePending(false) })
	lines := listenLines(t, sock)
	blob, err := json.Marshal(bridgeState{WakeToken: "carried-token", WakeSince: 4, WakePending: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(bridgeStateEnv, string(blob))
	out := &syncWriter{w: bufio.NewWriter(io.Discard)}

	// The probe first: with the switch on, the same handoff restores the
	// watcher and delivers the owed notice, so a quiet socket below means
	// the switch held and not that nothing was listening.
	ctx0, cancel0 := context.WithCancel(context.Background())
	var on inboxWatcher
	var streams0 sync.WaitGroup
	restoreCarried(ctx0, &http.Client{}, "http://127.0.0.1:1/mcp", "secret", out, &streams0, &on, true)
	if got := collect(lines, 2, 2*time.Second); len(got) != 2 {
		t.Fatalf("setup: %d line(s) with sockets on, want 2", len(got))
	}
	onTok := ""
	if ts := on.tokens(); len(ts) > 0 {
		onTok = ts[0]
	}
	if onTok != "carried-token" {
		t.Fatalf("setup: with sockets on the watcher holds %q", onTok)
	}
	cancel0()

	// The same handoff, into an image that read sockets = false.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var off inboxWatcher
	var streams sync.WaitGroup
	restoreCarried(ctx, &http.Client{}, "http://127.0.0.1:1/mcp", "secret", out, &streams, &off, false)
	if got := collect(lines, 1, 500*time.Millisecond); len(got) != 0 {
		t.Fatalf("with [wake] sockets = false the upgraded bridge put %d line(s) into its session: "+
			"the owed notice was delivered past the switch", len(got))
	}
	offTok := ""
	if ts := off.tokens(); len(ts) > 0 {
		offTok = ts[0]
	}
	if offTok != "" {
		t.Fatalf("with [wake] sockets = false the upgraded bridge restored the watcher under %q: "+
			"it will keep waking its session on every notice", offTok)
	}
}

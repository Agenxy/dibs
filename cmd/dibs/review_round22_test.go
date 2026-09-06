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

// A notice deferred to the cooldown when the image is replaced is delivered
// by the next image: the timer died with the old process and the cursor had
// already passed the event.
func TestADeferredNoticeSurvivesAnInPlaceUpgrade(t *testing.T) {
	sock := sockPath(t)
	t.Setenv("CLAUDE_CODE_MESSAGING_SOCKET", sock)
	t.Setenv("CLAUDE_CODE_MESSAGING_TOKEN", "child-token")
	t.Cleanup(func() { recordWakePending(false) })
	lines := listenLines(t, sock)
	// The old image: one notice delivered, a second arrival deferred to a
	// cooldown that will never fire in this process.
	old := &selfWaker{socket: sock, token: "child-token", cooldown: time.Hour}
	if err := old.wake(selfWakeNotice); err != nil {
		t.Fatal("setup:", err)
	}
	if err := old.wake(selfWakeNotice); err != nil {
		t.Fatal("setup:", err)
	}
	if got := collect(lines, 2, 2*time.Second); len(got) != 2 {
		t.Fatalf("setup: %d line(s) from the first notice, want 2", len(got))
	}
	if !handoffState().WakePending {
		t.Fatal("the handoff does not say a notice is owed")
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
	if err := json.Unmarshal([]byte(blob), &carried); err != nil || !carried.WakePending {
		t.Fatalf("the owed notice is not in the handoff: %+v %v", carried, err)
	}
	// The new image.
	t.Setenv(bridgeStateEnv, blob)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var iw inboxWatcher
	var streams sync.WaitGroup
	out := &syncWriter{w: bufio.NewWriter(io.Discard)}
	restoreCarried(ctx, &http.Client{}, "http://127.0.0.1:1/mcp", "secret", out, &streams, &iw)
	if got := collect(lines, 2, 2*time.Second); len(got) != 2 {
		t.Fatalf("after the upgrade %d line(s) arrived, want 2: the notice the old image owed is "+
			"gone with its timer, and the cursor has passed the event", len(got))
	}
}

// A delivery that failed arms a retry, and the handoff says so for as long
// as that retry is armed: an upgrade in that window must deliver the notice.
func TestAFailedDeliverysRetryIsOwedInTheHandoff(t *testing.T) {
	t.Cleanup(func() { recordWakePending(false) })
	sock := sockPath(t)
	w := &selfWaker{socket: sock, token: "tok", cooldown: time.Hour}
	if err := w.wake(selfWakeNotice); err == nil {
		t.Fatal("setup: a wake with nobody listening reported success")
	}
	if !currentWakePending() {
		t.Fatal("a failed delivery armed a retry and the handoff says nothing is owed: an " +
			"upgrade before the retry fires loses the notice with the timer")
	}
}

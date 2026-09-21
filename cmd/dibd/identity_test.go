package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/ledger"
	"github.com/agenxy/dibs/internal/supgang"
)

// engineNamed is an engine whose board carries the given node id, with a
// ledger of its own: identifyHost only reads and sets the host identity,
// so nothing here needs the loop running.
func engineNamed(t *testing.T, node string) *engine.Engine {
	t.Helper()
	dir := t.TempDir()
	box, err := ledger.LoadOrCreateKey(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	led, err := ledger.Open(filepath.Join(dir, "ledger.jsonl"), node, box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = led.Close() })
	return engine.New(core.NewState(node, core.DefaultLimits()), led, nil)
}

// A daemon keeps the identity this computer is already known by when
// Supgang does not answer.
//
// It kept the board's own node id instead, while every bridge here carried
// the Supgang one (which the bridge remembers across a failed lookup since
// round twenty-eight): the fold then reads this machine's own agents as
// remote, so the daemon refuses its own local wake commands for them and
// no host bridge exists to take over, and nothing retried. Round
// twenty-nine of the pre-release review.
func TestADaemonKeepsTheIdentityThisComputerIsKnownBy(t *testing.T) {
	const node = "a8a37e32c37cdf4fb7634de622bc3f84ccb4636580d1ea26af3fe8ac31d1f152"
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, supgang.NodeIDFile), []byte(node+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Supgang installed and not answering: the command exists and fails.
	old := supgang.Command
	supgang.Command = filepath.Join(dir, "supgang-that-fails")
	t.Cleanup(func() { supgang.Command = old })
	if err := os.WriteFile(supgang.Command, []byte("#!/usr/bin/env false\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	eng := engineNamed(t, "board-node")
	if done := identifyHost(eng, dir); done {
		t.Fatal("a failed lookup reported identification as settled, so nothing would retry")
	}
	if got := eng.HostID(); got != node {
		t.Fatalf("the daemon calls this computer %q, and its bridges call it %q: their agents read "+
			"as remote to it, so it refuses its own local wake commands for them", got, node)
	}
	// Nothing remembered: the board's own node id stands, as before.
	fresh := t.TempDir()
	eng2 := engineNamed(t, "board-node")
	identifyHost(eng2, fresh)
	if got := eng2.HostID(); got != "board-node" {
		t.Fatalf("with nothing remembered the daemon calls itself %q, want its own node id", got)
	}
	// AND THE TWO HALVES AGREE, which is the property: the bridge reads
	// the same remembered file from the same data directory (cmd/dibs,
	// hostID), so a machine whose Supgang is down is still one machine.
	// `dibs` is a separate binary, so this asserts on the file both read
	// rather than calling across the two mains.
	if got := supgang.RememberedNodeID(dir); got != eng.HostID() {
		t.Fatalf("the daemon answers %q and the file every bridge here reads says %q", eng.HostID(), got)
	}
	if supgang.RememberedNodeID(fresh) != "" {
		t.Fatal("a directory with no successful lookup remembered something")
	}
}

// And a daemon that could not identify itself at startup does not change
// its identity while it is serving.
//
// The first retry called SetHostID from its goroutine. Two defects in one:
// the identity decides which agents are on this machine, so changing it
// under a board that already holds claims splits it (rows registered
// before and after read as two computers, and both can take one path
// exclusively); and the setter writes a plain field the request path
// reads, which is a data race. The retry now remembers the answer, so the
// next start is right, and says a restart would pick it up. Round thirty
// of the pre-release review.
func TestADaemonDoesNotChangeItsIdentityWhileServing(t *testing.T) {
	const node = "a8a37e32c37cdf4fb7634de622bc3f84ccb4636580d1ea26af3fe8ac31d1f152"
	dir := t.TempDir()
	// Supgang answers now, and did not when this daemon started.
	old := supgang.Command
	supgang.Command = filepath.Join(dir, "supgang")
	t.Cleanup(func() { supgang.Command = old })
	script := "#!/bin/sh\nprintf '%s' '{\"schema\":\"supgang.status/v4\",\"status\":\"ok\"," +
		"\"name\":\"MacSolis\",\"node_id\":\"" + node + "\"}'\n"
	if err := os.WriteFile(supgang.Command, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	eng := engineNamed(t, "board-node")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	oldRetry := identifyRetry
	identifyRetry = 10 * time.Millisecond
	t.Cleanup(func() { identifyRetry = oldRetry })
	keepAskingSupgang(ctx, eng, dir, false)

	// The answer is remembered for the next start, and the running daemon
	// keeps the identity it has been serving under all along.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && supgang.RememberedNodeID(dir) == "" {
		time.Sleep(10 * time.Millisecond)
	}
	if got := supgang.RememberedNodeID(dir); got != node {
		t.Fatalf("the retry remembered %q, want %q: the next start would identify wrongly again", got, node)
	}
	if got := eng.HostID(); got != "board-node" {
		t.Fatalf("the daemon changed its identity to %q while serving: agents registered before and "+
			"after read as two computers, and both can take one path exclusively", got)
	}
}

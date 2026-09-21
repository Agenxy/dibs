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

// boardNode is the ledger id these tests give a board: the identity a
// daemon serves under when Supgang has never named this computer.
const boardNode = "board-node"

// engineNamed is an engine whose board carries boardNode, with a ledger of
// its own: identifyHost only reads and sets the host identity, so nothing
// here needs the loop running.
func engineNamed(t *testing.T) *engine.Engine {
	t.Helper()
	node := boardNode
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

	eng := engineNamed(t)
	if done, _ := identifyHost(eng, dir); done {
		t.Fatal("a failed lookup reported identification as settled, so nothing would retry")
	}
	if got := eng.HostID(); got != node {
		t.Fatalf("the daemon calls this computer %q, and its bridges call it %q: their agents read "+
			"as remote to it, so it refuses its own local wake commands for them", got, node)
	}
	// AND SUPGANG UNINSTALLED IS A FAILED LOOKUP TOO. The bridge keeps the
	// remembered id whatever the reason; the daemon returned early here
	// and served under its ledger id, so the two disagreed again. Round
	// thirty-one of the pre-release review.
	gone := engineNamed(t)
	supgang.Command = filepath.Join(dir, "no-supgang-here")
	if done, _ := identifyHost(gone, dir); !done {
		t.Error("an uninstalled Supgang is something to wait for: nothing will ever answer")
	}
	if got := gone.HostID(); got != node {
		t.Fatalf("with Supgang uninstalled the daemon calls this computer %q while its bridges "+
			"call it %q: it reads its own agents as remote and skips its own wake commands", got, node)
	}

	// Nothing remembered: the board's own node id stands, as before.
	fresh := t.TempDir()
	eng2 := engineNamed(t)
	_, _ = identifyHost(eng2, fresh)
	if got := eng2.HostID(); got != boardNode {
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

	eng := engineNamed(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	oldRetry := identifyRetry
	identifyRetry = 10 * time.Millisecond
	t.Cleanup(func() { identifyRetry = oldRetry })
	keepAskingSupgang(ctx, eng, false)

	// The running daemon keeps the identity it has been serving under, and
	// nothing else on this machine is told otherwise: remembering the new
	// id here would hand it to the next bridge to start, which is the same
	// split from the other side. The daemon that ADOPTS the identity is the
	// one that remembers it, at startup. Round thirty-four of the
	// pre-release review.
	time.Sleep(200 * time.Millisecond) // several ticks of the shortened retry
	if got := eng.HostID(); got != boardNode {
		t.Fatalf("the daemon changed its identity to %q while serving: agents registered before and "+
			"after read as two computers, and both can take one path exclusively", got)
	}
	if got := supgang.RememberedNodeID(dir); got != "" {
		t.Fatalf("the retry remembered %q while this daemon serves under %q: the next bridge to "+
			"start reads that and the machine is split again", got, boardNode)
	}
	// And a daemon STARTING now adopts it and remembers it, which is the
	// transition: one restart, everything agrees.
	fresh := engineNamed(t)
	if settled, _ := identifyHost(fresh, dir); !settled {
		t.Fatal("a startup lookup that Supgang answered reported itself unsettled")
	}
	if got, remembered := fresh.HostID(), supgang.RememberedNodeID(dir); got != node || remembered != node {
		t.Fatalf("a daemon starting with Supgang up serves under %q and remembered %q, want %q both", got, remembered, node)
	}
}

// A daemon that falls back to the identity this computer is known by still
// recognises the ids it used to answer to.
//
// Round thirty-five gave the Supgang-answered path its aliases and round
// thirty-six gave it the rename, for exactly one reason: a machine that
// ran Dibs before joining the fleet has rows, claims and long-lived
// bridges carrying the id it minted, and treating those as another
// computer lets two agents here hold one path exclusively. The fallback
// paths adopt the SAME fleet id from the remembered file and did neither,
// so the split came back the first time Supgang was down or uninstalled at
// startup: a bridge still asserting the ledger id registered agents that
// the fold read as remote. Round thirty-eight of the pre-release review.
func TestAFallbackIdentityStillRecognisesThisComputersOldIds(t *testing.T) {
	const node = "a8a37e32c37cdf4fb7634de622bc3f84ccb4636580d1ea26af3fe8ac31d1f152"
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, supgang.NodeIDFile), []byte(node+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := supgang.Command
	supgang.Command = filepath.Join(dir, "no-supgang-here") // uninstalled
	t.Cleanup(func() { supgang.Command = old })

	eng := engineNamed(t)
	if settled, _ := identifyHost(eng, dir); !settled {
		t.Fatal("an uninstalled Supgang is nothing to wait for")
	}
	if got := eng.HostID(); got != node {
		t.Fatalf("the daemon serves under %q, want the remembered fleet id", got)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	// Two agents here: one from a bridge started since the restart, one
	// from a bridge that predates it and still asserts the ledger id.
	reg := func(name, host string) string {
		t.Helper()
		res, err := eng.Do(ctx, &core.Op{
			Kind: core.OpRegister, Name: name, SessionID: "session-" + name,
			Agent: &core.AgentInfo{HostID: host},
		})
		if err != nil {
			t.Fatalf("setup: register %s: %v", name, err)
		}
		tok, _ := res["token"].(string)
		if _, err := eng.Do(ctx, &core.Op{Kind: core.OpAckBoard, Token: tok}); err != nil {
			t.Fatalf("setup: ack %s: %v", name, err)
		}
		return tok
	}
	claims := func(tok string) bool {
		t.Helper()
		res, err := eng.Do(ctx, &core.Op{
			Kind: core.OpClaim, Token: tok, Path: "/one/path", Mode: core.ClaimExclusive,
		})
		if err != nil {
			t.Fatalf("setup: claim: %v", err)
		}
		granted, _ := res["granted"].(bool)
		return granted
	}
	if !claims(reg("since-the-restart", node)) {
		t.Fatal("setup: the first exclusive claim was refused")
	}
	if claims(reg("older-bridge", boardNode)) {
		t.Fatal("two agents on this computer hold one path exclusively: the daemon adopted the " +
			"fleet id from the remembered file without recognising the id it used to answer to")
	}
}

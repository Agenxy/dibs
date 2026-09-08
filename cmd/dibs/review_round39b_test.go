package main

import (
	"os"
	"testing"

	"github.com/agenxy/dibs/internal/paths"
)

// A daemon launched with `-addr https://127.0.0.1:4777` registered the bare
// listener, and the upgrade, which rebuilds the replacement's argv from the
// registry, restarted it with a bare address the replacement re-inferred: an
// https loopback board came back plaintext and every client lost it. The
// registry carries the scheme the daemon was asked for, and the upgrade
// hands it back.
func TestAnUpgradeKeepsTheSchemeTheDaemonWasAskedFor(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // the registry lives under HOME
	dir := t.TempDir()
	release, err := paths.Claim(paths.Daemon{PID: os.Getpid(), Addr: "127.0.0.1:4777", Dir: dir, Scheme: "https"}, true)
	if err != nil {
		t.Fatal("setup:", err)
	}
	defer release()
	was := runningDaemon(dir)
	if was.unknown || was.addr == "" {
		t.Fatalf("setup: the registered daemon was not found: %+v", was)
	}
	if was.addr != "https://127.0.0.1:4777" {
		t.Fatalf("the running daemon is described as %q: the transport it was asked for is gone, "+
			"and the replacement will re-infer plaintext for loopback", was.addr)
	}
	if got := replacementAddr(dir, was.addr); got != "https://127.0.0.1:4777" {
		t.Fatalf("the replacement is started with %q", got)
	}
}

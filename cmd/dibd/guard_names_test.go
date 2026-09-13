package main

import (
	"path/filepath"
	"testing"
)

// A browser that reached the board by the name Remap routes to it sends that
// name as its origin, on Remap's gateway port rather than the daemon's, and
// the gate treats it as this board's own; every other name stays a stranger.
func TestAConfiguredBoardNameIsThisBoardsOrigin(t *testing.T) {
	g := newAuthGate("s", filepath.Join(t.TempDir(), "admin.hash"), "127.0.0.1:4777")
	if g.localOrigin("http://dibs", "dibs") {
		t.Fatal("an unconfigured name was accepted as this board's origin")
	}
	g.SetNames("dibs", " board.lab ", "")
	for _, o := range []string{"http://dibs", "http://DIBS", "http://dibs:80", "https://board.lab", "https://board.lab:443"} {
		if !g.localOrigin(o, "dibs") {
			t.Errorf("origin %s refused after the name was configured", o)
		}
	}
	// The gateway's origin and no other: a page on another port merely
	// shares the hostname, and a prefix or suffix is another name.
	for _, o := range []string{"http://dibs:8080", "https://dibs:4443", "http://dibs.evil", "http://notdibs", "http://evil/dibs"} {
		if g.localOrigin(o, "dibs") {
			t.Errorf("origin %s accepted: a name is the gateway's origin, exactly", o)
		}
	}
	if !g.localOrigin("http://127.0.0.1:4777", "127.0.0.1:4777") {
		t.Error("the address stopped being an origin once a name was configured")
	}
}

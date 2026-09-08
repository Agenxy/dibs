package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A joining machine runs doctor against a remote board with a data
// directory of its own, and the wake check read that directory's dibs.toml
// as the board's wake configuration, with a repair that edits a file the
// hub never reads. The node id says whose board this is.
func TestDoctorDoesNotReadTheLocalWakeConfigForARemoteBoard(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "node_id"), []byte("local-1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A local file that WOULD claim coverage if it were read.
	if err := os.WriteFile(filepath.Join(dir, "dibs.toml"), []byte("[wake.exec.codex]\nargv = [\"codex\", \"exec\", \"resume\", \"{thread}\", \"{message}\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var what, fix string
	checkWakeRoutes(dir, &boardView{Node: "hub-9"},
		func(msg string) { t.Fatalf("a remote board's wake coverage was reported from the local file: %s", msg) },
		func(w, f string) { what, fix = w, f })
	if !strings.Contains(what, "another daemon") || !strings.Contains(what, "hub-9") {
		t.Fatalf("the remote board is not named as somebody else's: %q", what)
	}
	if !strings.Contains(fix, "on the machine that runs the daemon") {
		t.Fatalf("the repair does not send the operator to the hub: %q", fix)
	}
	// The same directory serving its own board is checked as before.
	called := false
	checkWakeRoutes(dir, &boardView{Node: "local-1"}, func(string) { called = true }, func(string, string) { called = true })
	if !called {
		t.Fatal("a board served from this directory was not checked at all")
	}
}

// With [wake] sockets = false and no [wake.exec] command, neither route
// runs; the check said the socket is tried first and called delivery
// unconfirmed when it was off.
func TestDoctorSaysWhenBothWakeRoutesAreOff(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "node_id"), []byte("local-1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "dibs.toml"), []byte("[wake]\nsockets = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var what, fix string
	checkWakeRoutes(dir, &boardView{Node: "local-1"},
		func(msg string) { t.Fatalf("no route at all reported as fine: %s", msg) },
		func(w, f string) { what, fix = w, f })
	if !strings.Contains(what, "no wake route at all") {
		t.Fatalf("with sockets off and no command the report says %q", what)
	}
	if strings.Contains(fix, "tried first") {
		t.Fatalf("the report says the socket is tried first while it is switched off: %q", fix)
	}
}

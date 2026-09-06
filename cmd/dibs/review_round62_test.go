package main

import (
	"os"
	"path/filepath"
	"testing"
)

// Recovery starts the build just installed and the report says so; restarting
// through a unit whose ExecStart still names the OLD binary makes that a lie.
// startDaemon must recognise a unit that pins a different binary and start
// directly with the installed one, the same way it handles a unit that names
// the wrong data directory.
func TestAUnitPinningAStaleBinaryIsRecognised(t *testing.T) {
	dir := t.TempDir()
	installed := filepath.Join(dir, "new", "dibd")
	stale := filepath.Join(dir, "old", "dibd")
	for _, p := range []string{installed, stale} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	plist := func(daemon string) string {
		p := filepath.Join(t.TempDir(), "org.agenxy.dibs.plist")
		body := "<plist><dict><key>ProgramArguments</key><array>" +
			"<string>" + daemon + "</string><string>-dir</string><string>" + dir + "</string>" +
			"</array></dict></plist>"
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	// A unit that pins the stale binary is unfit: recovery must not restart it,
	// or it brings the old build back and calls it the new one.
	if reason := unitUnfitToRestart(plist(stale), dir, installed); reason == "" {
		t.Fatal("a unit pinning the old binary was judged fit to restart: recovery would " +
			"start the old build and the report would call it the new one")
	}
	// A unit that pins the installed binary and names this directory is fit.
	if reason := unitUnfitToRestart(plist(installed), dir, installed); reason != "" {
		t.Fatalf("a unit pinning the installed binary was judged unfit (%q): recovery would "+
			"needlessly abandon a correct service unit", reason)
	}
}

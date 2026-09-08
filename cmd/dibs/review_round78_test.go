package main

import (
	"os"
	"path/filepath"
	"testing"
)

// The parser took the first absolute path ending in `dibd`, wherever it
// appeared, so a unit's OTHER directives answered for the executable. A
// WorkingDirectory that happens to end that way sits above ExecStart in a
// perfectly ordinary unit, and the drift check then calls a correct unit wrong
// and reconcile rewrites it, discarding whatever the operator tuned there.
func TestTheUnitsExecutableIsReadFromItsOwnDirective(t *testing.T) {
	dir := t.TempDir()
	installed := filepath.Join(dir, "opt", "dibs", "bin", "dibd")
	if err := os.MkdirAll(filepath.Dir(installed), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(installed, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	board := filepath.Join(dir, "srv", "board")
	if err := os.MkdirAll(board, 0o755); err != nil {
		t.Fatal(err)
	}
	// A directory that ends in the daemon's name, named by an unrelated key.
	decoy := filepath.Join(dir, "srv", "dibd")
	if err := os.MkdirAll(decoy, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Run("systemd, with a decoy directive above ExecStart", func(t *testing.T) {
		unit := filepath.Join(t.TempDir(), "dibs.service")
		body := "[Service]\n" +
			"WorkingDirectory=" + decoy + "\n" +
			"ExecStart=" + systemdArg(installed) + " -dir " + systemdArg(board) + "\n"
		if err := os.WriteFile(unit, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := unitBinary(unit); got != installed {
			t.Fatalf("unitBinary read %q, want the ExecStart program %q: an unrelated "+
				"directive answered for the executable", got, installed)
		}
		if reason := unitUnfitToRestart(unit, board, installed); reason != "" {
			t.Errorf("a correct unit was judged unfit (%q), so upgrade would rewrite it and "+
				"discard the operator's settings", reason)
		}
	})

	t.Run("systemd, with a prefixed ExecStart", func(t *testing.T) {
		unit := filepath.Join(t.TempDir(), "dibs.service")
		// A leading '-' tells systemd to ignore a failure; it is not part of
		// the path.
		body := "[Service]\nExecStart=-" + systemdArg(installed) + " -dir " + systemdArg(board) + "\n"
		if err := os.WriteFile(unit, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := unitBinary(unit); got != installed {
			t.Errorf("unitBinary read %q, want %q: the directive's prefix was taken for "+
				"part of the path", got, installed)
		}
	})

	t.Run("launchd, with a decoy string before the program", func(t *testing.T) {
		unit := filepath.Join(t.TempDir(), "org.agenxy.dibs.plist")
		body := "<plist><dict>" +
			"<key>WorkingDirectory</key><string>" + decoy + "</string>" +
			"<key>ProgramArguments</key><array>" +
			"<string>" + installed + "</string><string>-dir</string><string>" + board + "</string>" +
			"</array></dict></plist>"
		if err := os.WriteFile(unit, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := unitBinary(unit); got != installed {
			t.Fatalf("unitBinary read %q, want the first ProgramArguments entry %q", got, installed)
		}
	})
}

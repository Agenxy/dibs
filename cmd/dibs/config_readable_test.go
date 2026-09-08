package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The bridge proceeds on a key it does not know, and refuses a file that does
// not parse.
//
// The bridge is the binary a session started with, so between a `task install`
// and that session's restart it is older than the daemon. It refused a
// `fallback` key the daemon had accepted and was running on, and told the
// operator "the daemon will not start on it either". A program that is not the
// file's authority does not get to speak for the one that is; the daemon
// refuses unknown keys itself, and if it has, the connection fails and says so.
func TestTheBridgeProceedsOnAKeyItDoesNotKnow(t *testing.T) {
	dir := t.TempDir()
	toml := "[wake.exec.codex]\nargv = [\"codex\"]\nkey_from_a_newer_daemon = true\n"
	if err := os.WriteFile(filepath.Join(dir, "dibs.toml"), []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := configReadable(dir); err != nil {
		t.Fatalf("refused a file the daemon may be running on, over a key this build "+
			"does not know: %v", err)
	}
}

// And still refuses what really cannot be read, with the reasons.
func TestTheBridgeStillRefusesAFileThatDoesNotParse(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "dibs.toml"), []byte("[wake\nnot toml"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := configReadable(dir)
	if err == nil {
		t.Fatal("proceeded on a file that does not parse: the address is a guess and " +
			"every request would carry the local secret to whatever answers")
	}
	if !strings.Contains(err.Error(), "cannot be read") {
		t.Errorf("the refusal does not say what is wrong: %v", err)
	}
}

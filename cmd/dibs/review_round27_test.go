package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// [wake] sockets = false switches the bridge's self-wake off with the
// daemon's peer-socket route; the guide promised a configuration with no
// unsolicited activations and this is the half it was missing.
func TestSocketsOffSwitchesTheBridgesSelfWakeOff(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "local.secret"), []byte(strings.Repeat("a", 64)), 0o600); err != nil {
		t.Fatal(err)
	}
	if !socketWakesOnIn(dir) {
		t.Fatal("with no dibs.toml the self-wake is off: the default is on")
	}
	if err := os.WriteFile(filepath.Join(dir, "dibs.toml"), []byte("[wake]\nsockets = false\n[future]\nx = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if socketWakesOnIn(dir) {
		t.Fatal("[wake] sockets = false left the bridge's self-wake on, past a setting this build does not know")
	}
}

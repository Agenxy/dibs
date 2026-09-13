package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The unit carries exactly the join recipe's variables and runs `dibs
// host-bridge` and nothing else; a data directory with no wake table, or no
// board address, gets no unit; and an existing unit is not overwritten.
func TestTheBridgeUnitCarriesTheJoinRecipeAndNothingElse(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	dir := filepath.Join(home, ".dibs-a8a37e32-4790")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DIBS_DIR", dir)
	t.Setenv("DIBS_ADDR", "https://192.168.1.191:4790")
	t.Setenv(boardPeerEnv, "a8a37e32")
	t.Setenv("DIBS_HOST_ID", "")
	if err := os.WriteFile(filepath.Join(dir, "local.secret"), []byte(strings.Repeat("a", 64)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := hostBridgeUnit(); err == nil || !strings.Contains(err.Error(), "no [wake.exec]") {
		t.Errorf("a bridge that can start nobody got a unit: %v", err)
	}
	table := "[wake.exec.codex]\nargv = [\"codex\", \"exec\", \"resume\", \"{thread}\", \"{message}\"]\n"
	if err := os.WriteFile(filepath.Join(dir, "dibs.toml"), []byte(table), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DIBS_ADDR", "")
	if err := hostBridgeUnit(); err == nil || !strings.Contains(err.Error(), "DIBS_ADDR") {
		t.Errorf("a unit with no board address was written: %v", err)
	}
	t.Setenv("DIBS_ADDR", "https://192.168.1.191:4790")

	env := map[string]string{"DIBS_ADDR": "https://192.168.1.191:4790", "DIBS_DIR": dir, boardPeerEnv: "a8a37e32"}
	for _, goos := range []string{"darwin", "linux"} {
		target, body, load, err := bridgeUnit(goos, "/opt/dibs/bin/dibs", dir, env)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(target, "dibs-bridge-dibs-a8a37e32-4790-") {
			t.Errorf("%s: the unit is not named after the board: %s", goos, target)
		}
		if goos == "linux" && (strings.Contains(load, "host-bridge.log") || !strings.Contains(load, "journalctl")) {
			t.Errorf("linux: the log destination promised is not where systemd sends it: %q", load)
		}
		if goos == "darwin" && !strings.Contains(load, filepath.Join(dir, "host-bridge.log")) {
			t.Errorf("darwin: the load instruction does not name the log: %q", load)
		}
		for _, want := range []string{"host-bridge", "/opt/dibs/bin/dibs", "https://192.168.1.191:4790", dir, "a8a37e32", "DIBS_BOARD_PEER"} {
			if !strings.Contains(body, want) {
				t.Errorf("%s: the unit lacks %q:\n%s", goos, want, body)
			}
		}
		if strings.Contains(body, strings.Repeat("a", 64)) || strings.Contains(body, "-dir") {
			t.Errorf("%s: the unit carries the secret, or the daemon's arguments:\n%s", goos, body)
		}
		if load == "" {
			t.Errorf("%s: no load instruction", goos)
		}
	}
	if _, _, _, err := bridgeUnit("windows", "dibs", dir, env); err == nil {
		t.Error("an unsupported platform got a unit")
	}

	// Written once, for this platform, and refused the second time.
	if err := hostBridgeUnit(); err != nil {
		t.Fatalf("hostBridgeUnit: %v", err)
	}
	target, _, _, _ := bridgeUnit(runtimeGOOS(), self(), dir, env)
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("the unit was not written at %s: %v", target, err)
	}
	if err := hostBridgeUnit(); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("an existing unit was overwritten: %v", err)
	}
	// Readable, and distinct for two directories that read the same.
	if s := bridgeUnitSlug("/x/.dibs-hub:4777"); !strings.HasPrefix(s, "dibs-hub-4777-") || len(s) != len("dibs-hub-4777-")+8 {
		t.Errorf("slug = %q", s)
	}
	if bridgeUnitSlug("/x/.dibs-hub:4777") == bridgeUnitSlug("/x/.dibs-hub-4777") || bridgeUnitSlug("/a/board") == bridgeUnitSlug("/b/board") {
		t.Error("two data directories share a unit name")
	}
}

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// A joined machine's bridge applies the same rule as the daemon.
//
// `dibs host-bridge` runs [wake.exec] on another machine for the hub. Two
// loaders and a rule in one would let a joined machine host the agent the hub
// refuses to, so this reads a real dibs.toml off disk through the bridge's own
// loader, with the recipe Dibs used to recommend in it.
func TestAJoinedMachineNeverRunsACommandThatHostsAnAgent(t *testing.T) {
	dir := t.TempDir()
	toml := `[wake.exec.codex]
argv     = ["codex", "exec", "resume", "{thread}", "{message}"]
fallback = ["codex", "queue", "--thread", "{thread}", "--message", "{message}"]

[wake.exec."claude code"]
argv = ["claude", "--resume", "{thread}", "-p", "{message}"]
`
	if err := os.WriteFile(filepath.Join(dir, "dibs.toml"), []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
	routes, err := localWakeRoutes(dir)
	if err != nil {
		t.Fatalf("the bridge refused a config whose Codex entry still delivers: %v", err)
	}
	if c := routes["codex"]; len(c.Argv) < 2 || c.Argv[1] != "queue" || len(c.Fallback) != 0 {
		t.Errorf("the bridge would run %v / %v for Codex; want `codex queue` alone", c.Argv, c.Fallback)
	}
	if _, kept := routes["claude code"]; kept {
		t.Error("the bridge kept `claude --resume -p`, which runs a Claude Code session of its own")
	}
}

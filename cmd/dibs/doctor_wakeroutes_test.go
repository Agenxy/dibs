package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An operator is told when the only wake route they have cannot be confirmed.
//
// There are two routes and they are not equals. `[wake.exec]` starts a process
// and the daemon sees its exit status. The session socket needs no setup and is
// best effort: the receiving harness decides whether to accept a peer message
// and sends no receipt, so a notice that was HELD looks exactly like one that
// was read.
//
// That difference was invisible, and doctor is the tool whose whole job is
// finding what is quietly broken. Measured on the machine this is developed on:
// notices delivered to an idle live session, every write succeeding, nothing
// arriving, because a Claude Code session in bypassPermissions mode holds peer
// messages for its human. An unattended fleet runs in exactly that mode.
func TestDoctorSaysWhichWakeRoutesExist(t *testing.T) {
	run := func(t *testing.T, toml string) (oks, warns []string) {
		t.Helper()
		dir := t.TempDir()
		if toml != "" {
			if err := os.WriteFile(filepath.Join(dir, "dibs.toml"), []byte(toml), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		checkWakeRoutes(dir,
			func(s string) { oks = append(oks, s) },
			func(s, fix string) { warns = append(warns, s+" || "+fix) })
		return
	}

	t.Run("no command configured", func(t *testing.T) {
		oks, warns := run(t, "")
		if len(warns) != 1 {
			t.Fatalf("expected one warning, got oks=%v warns=%v", oks, warns)
		}
		for _, want := range []string{"best effort", "bypassPermissions", "wake.exec"} {
			if !strings.Contains(warns[0], want) {
				t.Errorf("the warning does not mention %q, so it does not tell the "+
					"operator what is actually wrong: %s", want, warns[0])
			}
		}
	})

	t.Run("a command is configured", func(t *testing.T) {
		oks, warns := run(t, "[wake.exec.claude]\nargv = [\"echo\", \"{message}\"]\n")
		if len(warns) != 0 {
			t.Errorf("warned about a board that HAS a confirmable route: %v", warns)
		}
		if len(oks) != 1 || !strings.Contains(oks[0], "1 wake command") {
			t.Errorf("did not report the configured route: %v", oks)
		}
	})
}

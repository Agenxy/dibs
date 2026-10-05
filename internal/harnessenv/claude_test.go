package harnessenv

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func writeClaudeRecord(t *testing.T, home, local, cli string, archived bool) {
	t.Helper()
	dir := filepath.Join(home, "Library", "Application Support", "Claude", "claude-code-sessions", "acct", "org")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{"sessionId":"` + local + `","cliSessionId":"` + cli + `","isArchived":` + strconv.FormatBool(archived) + `}`
	if err := os.WriteFile(filepath.Join(dir, local+".json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The Claude app's link takes the APP's id for a session, so the mapping from
// a Claude Code session id is read from the app's own records, never guessed.
func TestAClaudeSessionIsOpenedByTheAppsOwnID(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	const cli = "6336088d-1111-4222-8333-444455556666"
	writeClaudeRecord(t, home, "local_08697f65-6786-4c61-96f8-9af1aa07b24e", cli, false)
	writeClaudeRecord(t, home, "local_aaaaaaaa-0000-4000-8000-000000000000", "11111111-2222-4333-8444-555566667777", true)

	got := OpenArgv(ClaudeDesktop, cli)
	if len(got) != 2 || got[1] != "claude://code/continue?session=local_08697f65-6786-4c61-96f8-9af1aa07b24e" {
		t.Fatalf("OpenArgv = %q", got)
	}
	if OpenArgv(ClaudeDesktop, "11111111-2222-4333-8444-555566667777") != nil {
		t.Error("an archived session was opened")
	}
	if OpenArgv(ClaudeDesktop, "99999999-2222-4333-8444-555566667777") != nil {
		t.Error("a session the app has no record of (a terminal one) was opened")
	}
	if ClaudeOpenArgv("local_x?y=z") != nil || ClaudeOpenArgv("../etc") != nil {
		t.Error("an id that is not the app's shape reached the link")
	}
}

// A session whose process is running needs no open: the socket reaches it.
func TestARunningClaudeSessionIsNotOpenedAgain(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".claude", "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	const cli = "6336088d-1111-4222-8333-444455556666"
	body := `{"pid":` + strconv.Itoa(os.Getpid()) + `,"sessionId":"` + cli + `"}`
	if err := os.WriteFile(filepath.Join(dir, "1.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if !ClaudeSessionRunning(cli) {
		t.Error("a session whose process is alive reads as not running")
	}
	// Through the shower production uses: a running session is held, so no
	// open is made and the app does not jump forward.
	if !RealShower.holds(cli) {
		t.Error("the production shower would open a Claude session that is already running")
	}
	// Also enter the production open door: its ownership dispatch must reach
	// ClaudeSessionRunning, not ChatGPT-only Ownership or the presence gate.
	s := RealShower
	s.Open = func([]string) error { t.Error("running Claude session was re-opened"); return nil }
	s.Away = func() (bool, bool) { t.Error("running Claude session reached away gate"); return true, true }
	s.ShowWhenIdle(ClaudeOpenArgv("local_live-fixture"), cli, func(opened, deferred bool, err error) {
		if opened || deferred || err != nil {
			t.Errorf("running Claude session entered open path: %v %v %v", opened, deferred, err)
		}
	})
	if ClaudeSessionRunning("99999999-2222-4333-8444-555566667777") {
		t.Error("a session with no file reads as running")
	}
}

// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

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

// The app gives a session a NEW Claude Code session id each time it is
// resumed and keeps the earlier ones in priorCliSessionIds; its own id
// (local_...) stays the same. Dibs knows an agent by the Claude Code session
// it last saw, so matching cliSessionId alone loses every agent whose session
// was resumed once: k7-dev, 2026-10-10, had its mail escalated to the person
// as unreachable while the app held "K7 Dev" with that id in its prior list.
// Shape copied from the installed app's record for that session.
func TestAResumedClaudeSessionIsFoundByAPriorCLISessionID(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, "Library", "Application Support", "Claude", "claude-code-sessions", "acct", "org")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	const (
		local = "local_fd69ccb7-6d52-481c-bbd9-ebede76dbdda"
		known = "9ba6f463-1ae8-4a40-884d-c4b5fed55179" // what Dibs recorded
		now   = "07b5f68d-c28c-4d3f-9615-6d6bff9d92c6" // after the app resumed it
	)
	body := `{"sessionId":"` + local + `","cliSessionId":"` + now + `","isArchived":false,` +
		`"priorCliSessionIds":["0f0e0d0c-1111-4222-8333-444455556666","` + known + `"]}`
	if err := os.WriteFile(filepath.Join(dir, local+".json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	// An archived record that also lists the id must never win.
	archived := `{"sessionId":"local_aaaaaaaa-0000-4000-8000-000000000000","cliSessionId":"22222222-2222-4333-8444-555566667777",` +
		`"isArchived":true,"priorCliSessionIds":["` + known + `"]}`
	if err := os.WriteFile(filepath.Join(dir, "local_aaaaaaaa-0000-4000-8000-000000000000.json"), []byte(archived), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{known, now} {
		if got := ClaudeLocalSession(id); got != local {
			t.Errorf("ClaudeLocalSession(%s) = %q, want %q", id, got, local)
		}
	}
}

// When one record names the id as CURRENT and another only as prior (the app
// forked a session), the current owner wins: that is the session the agent is
// actually running in.
func TestACurrentCLISessionBeatsAPriorMention(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, "Library", "Application Support", "Claude", "claude-code-sessions", "acct", "org")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	const cli = "6336088d-1111-4222-8333-444455556666"
	write := func(local, body string) {
		if err := os.WriteFile(filepath.Join(dir, local+".json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("local_bbbbbbbb-0000-4000-8000-000000000000",
		`{"sessionId":"local_bbbbbbbb-0000-4000-8000-000000000000","cliSessionId":"33333333-2222-4333-8444-555566667777","priorCliSessionIds":["`+cli+`"]}`)
	write("local_cccccccc-0000-4000-8000-000000000000",
		`{"sessionId":"local_cccccccc-0000-4000-8000-000000000000","cliSessionId":"`+cli+`"}`)
	if got := ClaudeLocalSession(cli); got != "local_cccccccc-0000-4000-8000-000000000000" {
		t.Errorf("ClaudeLocalSession = %q, want the record that runs it now", got)
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
	s.ShowWhenIdle(ClaudeOpenArgv("local_live-fixture"), cli, func(opened, deferred bool, err error) {
		if opened || deferred || err != nil {
			t.Errorf("running Claude session entered open path: %v %v %v", opened, deferred, err)
		}
	})
	if ClaudeSessionRunning("99999999-2222-4333-8444-555566667777") {
		t.Error("a session with no file reads as running")
	}
}

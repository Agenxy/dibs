package harnessenv

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"

	"github.com/agenxy/dibs/internal/liveness"
)

// Waking a closed Claude Code session in the Claude app.
//
// A Claude Code session in the desktop app is reached through its own socket,
// which exists only while its process runs. The app keeps that process alive
// while the app is open, so the case left is a session whose process has
// ended: the app was quit, or the session was never reopened after a restart.
// For that the app has its own link, claude://code/continue?session=<id>,
// which opens the session and starts its process. Measured 2026-10-01 on the
// installed app: a closed session's process was up 2 seconds after the link
// opened, and its SessionStart hook reached Dibs and resolved to its agent.
// The socket wake then delivers as it would to any running session.
//
// The link takes the APP's id for the session (local_...), not Claude Code's;
// the app keeps both in its session records, so the mapping is read, never
// guessed.

// processAlive is the cross-platform liveness check the daemon already uses:
// syscall.Kill does not exist on Windows, and that broke the build there.
var processAlive = liveness.New()

// localSessionID is the shape the app accepts, from its own link handler.
var localSessionID = regexp.MustCompile(`^local_[A-Za-z0-9-]{1,64}$`)

func claudeSupportDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "Library", "Application Support", "Claude", "claude-code-sessions")
}

// ClaudeLocalSession is the Claude app's id for a Claude Code session, or ""
// when the app has no record of it (a terminal session, say) or it is archived.
func ClaudeLocalSession(cliSession string) string {
	dir := claudeSupportDir()
	if dir == "" || !isThreadID(cliSession) {
		return ""
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*", "*", "local_*.json"))
	for _, f := range files {
		raw, err := os.ReadFile(f) //nolint:gosec // the app's own records, under the user's home
		if err != nil {
			continue
		}
		var rec struct {
			SessionID    string `json:"sessionId"`
			CLISessionID string `json:"cliSessionId"`
			IsArchived   bool   `json:"isArchived"`
		}
		if json.Unmarshal(raw, &rec) != nil || rec.CLISessionID != cliSession || rec.IsArchived {
			continue
		}
		if localSessionID.MatchString(rec.SessionID) {
			return rec.SessionID
		}
	}
	return ""
}

// ClaudeOpenArgv opens a session in the Claude app by its app id.
func ClaudeOpenArgv(local string) []string {
	if !localSessionID.MatchString(local) {
		return nil
	}
	return []string{"/usr/bin/open", "claude://code/continue?session=" + local}
}

// ClaudeSessionRunning reports whether a Claude Code session's process is
// alive, from the session files Claude Code writes under ~/.claude/sessions.
func ClaudeSessionRunning(cliSession string) bool {
	home, err := os.UserHomeDir()
	if err != nil || cliSession == "" {
		return false
	}
	files, _ := filepath.Glob(filepath.Join(home, ".claude", "sessions", "*.json"))
	for _, f := range files {
		raw, err := os.ReadFile(f) //nolint:gosec // Claude Code's own session files
		if err != nil {
			continue
		}
		var s struct {
			PID       int    `json:"pid"`
			SessionID string `json:"sessionId"`
		}
		if json.Unmarshal(raw, &s) != nil || s.SessionID != cliSession || s.PID <= 0 {
			continue
		}
		if processAlive.Alive(s.PID) {
			return true
		}
	}
	return false
}

// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

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
	return ClaudeAppSession("", cliSession)
}

// IsClaudeAppSession reports whether s has the shape of the app's own session
// id (local_...), the only shape its continue link accepts.
func IsClaudeAppSession(s string) bool { return localSessionID.MatchString(s) }

// ClaudeAppSession finds the app session that a Claude Code session belongs
// to, by LINEAGE rather than by its current id.
//
// The app's id for a session (local_...) is stable; the Claude Code session id
// inside it is not. Each resume starts a new one and the app moves the old one
// into priorCliSessionIds. Dibs knows an agent by the Claude Code session it
// last saw, so matching only the current id lost every agent whose session had
// been resumed even once: k7-dev on 2026-10-10 was escalated to the person as
// unreachable while the app still held its session, "K7 Dev", with that id in
// its prior list. A record matches when ANY id in its lineage is known.
//
// hint is the app session the agent's own bridge reported (AgentInfo
// AppSession). It is a claim, so it is honoured only when that record's
// lineage contains a known id: an agent cannot point Dibs at somebody else's
// session. Otherwise a record running a known id now beats one that only
// lists it as prior, and among those the most recently active wins.
func ClaudeAppSession(hint string, known ...string) string {
	dir := claudeSupportDir()
	want := map[string]bool{}
	for _, k := range known {
		if isThreadID(k) {
			want[k] = true
		}
	}
	if dir == "" || len(want) == 0 {
		return ""
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*", "*", "local_*.json"))
	best, bestRank, bestActivity := "", 0, int64(-1)
	for _, f := range files {
		rec, ok := readClaudeRecord(f)
		if !ok {
			continue
		}
		rank := rec.lineageRank(want)
		if rank == 0 {
			continue
		}
		if hint != "" && rec.SessionID == hint {
			return rec.SessionID // the bridge's claim, confirmed by the app's own lineage
		}
		if rank > bestRank || (rank == bestRank && rec.LastActivityAt > bestActivity) {
			best, bestRank, bestActivity = rec.SessionID, rank, rec.LastActivityAt
		}
	}
	return best
}

// claudeRecord is the part of the app's session record the mapping reads.
type claudeRecord struct {
	SessionID          string   `json:"sessionId"`
	CLISessionID       string   `json:"cliSessionId"`
	PriorCLISessionIDs []string `json:"priorCliSessionIds"`
	IsArchived         bool     `json:"isArchived"`
	LastActivityAt     int64    `json:"lastActivityAt"`
}

// readClaudeRecord reads one record, refusing an archived one and an id the
// app's link would not accept.
func readClaudeRecord(f string) (claudeRecord, bool) {
	var rec claudeRecord
	raw, err := os.ReadFile(f) //nolint:gosec // the app's own records, under the user's home
	if err != nil || json.Unmarshal(raw, &rec) != nil || rec.IsArchived || !localSessionID.MatchString(rec.SessionID) {
		return rec, false
	}
	return rec, true
}

// lineageRank is 2 when the record runs a known id now, 1 when it lists one
// as prior, and 0 when its lineage holds none of them.
func (r claudeRecord) lineageRank(want map[string]bool) int {
	if want[r.CLISessionID] {
		return 2
	}
	for _, p := range r.PriorCLISessionIDs {
		if want[p] {
			return 1
		}
	}
	return 0
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

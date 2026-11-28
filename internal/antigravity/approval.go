// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package antigravity

import (
	"context"
	"errors"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/procexec"
)

// ConversationState is the metadata-only evidence available for an attested
// conversation. A missing or inconsistent observation is unknown.
type ConversationState string

// Conversation observations report only the turn or approval state.
const (
	ConversationUnknown  ConversationState = ""
	ConversationBusy     ConversationState = "busy"
	ConversationApproval ConversationState = "approval"
	ConversationQuiet    ConversationState = "quiet"
)

const recentWALWindow = 3 * time.Minute

// A pending step (1) can follow a waiting command. Running (2), generating
// (8) and queued (11) steps are evidence of a current turn. DONE (3) is the
// terminal state measured in the app.
const conversationQuery = `
SELECT coalesce((
  SELECT CASE
    WHEN status IN (2, 8, 11) THEN 'busy'
    WHEN step_type = 132 AND status = 9 THEN 'approval'
    WHEN status = 3 THEN 'quiet'
    ELSE ''
  END
  FROM (SELECT idx, step_type, status FROM steps ORDER BY idx DESC LIMIT 32)
  WHERE status IS NULL OR status != 1
  ORDER BY idx DESC LIMIT 1
), '');`

// ObserveConversation uses recent WAL activity or an immutable database
// snapshot. The app's agentapi metadata does not expose step state.
func ObserveConversation(ctx context.Context, conversation string) ConversationState {
	if !core.LooksLikeThreadID(conversation) {
		return ConversationUnknown
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ConversationUnknown
	}
	path := filepath.Join(home, ".gemini", "antigravity", "conversations", conversation+".db")
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() {
		return ConversationUnknown
	}
	if recentConversationWAL(path, time.Now()) {
		return ConversationBusy
	}
	if !quietConversationDatabase(path) {
		return ConversationUnknown
	}
	return readConversationSnapshot(ctx, path, before)
}

func readConversationSnapshot(ctx context.Context, path string, before fs.FileInfo) ConversationState {
	uri := url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro&immutable=1"}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// #nosec G204 G702 -- system sqlite, fixed query and UUID-validated local path.
	cmd := procexec.Command(ctx, "/usr/bin/sqlite3", "-batch", "-bail", "-init", os.DevNull,
		"-readonly", uri.String(), conversationQuery)
	cmd.Env = []string{}
	cmd.WaitDelay = 100 * time.Millisecond
	output, err := cmd.Output()
	if recentConversationWAL(path, time.Now()) {
		return ConversationBusy
	}
	if err != nil || !sameConversationSnapshot(path, before) {
		return ConversationUnknown
	}
	state := ConversationState(strings.TrimSpace(string(output)))
	switch state {
	case ConversationBusy, ConversationApproval, ConversationQuiet:
		return state
	default:
		return ConversationUnknown
	}
}

func recentConversationWAL(path string, now time.Time) bool {
	info, err := os.Lstat(path + "-wal")
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return false
	}
	age := now.Sub(info.ModTime())
	return age >= 0 && age <= recentWALWindow
}

func sameConversationSnapshot(path string, before fs.FileInfo) bool {
	after, err := os.Lstat(path)
	return err == nil && os.SameFile(before, after) && before.Size() == after.Size() &&
		before.ModTime().Equal(after.ModTime()) && quietConversationDatabase(path)
}

func quietConversationDatabase(path string) bool {
	// Immutable mode skips locking and recovery. A journal with bytes makes
	// the view ambiguous, including a WAL with a newer approval decision.
	for _, suffix := range []string{"-wal", "-journal"} {
		info, err := os.Lstat(path + suffix)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil || !info.Mode().IsRegular() || info.Size() != 0 {
			return false
		}
	}
	return true
}

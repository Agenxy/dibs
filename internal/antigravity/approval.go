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

// The measured 2.35.2 trajectory represents run_command as 132 and WAITING
// as 9. A pending (1) or queued (11) suffix does not supersede the blocking
// step. A more recent running, completed, or unknown step does.
const approvalQuery = `
SELECT coalesce((
  SELECT step_type = 132 AND status = 9
  FROM (SELECT idx, step_type, status FROM steps ORDER BY idx DESC LIMIT 32)
  WHERE status IS NULL OR status NOT IN (1, 11)
  ORDER BY idx DESC LIMIT 1
), 0);`

// WaitingForCommandApproval observes one attested local conversation. The
// app's agentapi metadata has no step state, so use an immutable view of a
// quiet database and keep only the positive observation.
func WaitingForCommandApproval(ctx context.Context, conversation string) bool {
	if !core.LooksLikeThreadID(conversation) {
		return false
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	path := filepath.Join(home, ".gemini", "antigravity", "conversations", conversation+".db")
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || !quietConversationDatabase(path) {
		return false
	}
	uri := url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro&immutable=1"}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// #nosec G204 G702 -- system sqlite, fixed query and UUID-validated local path.
	cmd := procexec.Command(ctx, "/usr/bin/sqlite3", "-batch", "-bail", "-init", os.DevNull,
		"-readonly", uri.String(), approvalQuery)
	cmd.Env = []string{}
	cmd.WaitDelay = 100 * time.Millisecond
	output, err := cmd.Output()
	return err == nil && strings.TrimSpace(string(output)) == "1" && sameApprovalSnapshot(path, before)
}

func sameApprovalSnapshot(path string, before fs.FileInfo) bool {
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

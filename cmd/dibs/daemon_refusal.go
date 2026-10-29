// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/agenxy/dibs/internal/boardconfig"
	"github.com/agenxy/dibs/internal/paths"
)

// A 401 proves that something answered, not why it refused the credential.
// The daemon's local-only fingerprint can separate a different board from a
// matching node with a different secret. Older daemons leave that unknown.
func daemonRefusal(resp *http.Response) (problem, hint string) {
	problem = "daemon answered at " + addr() + " with " + resp.Status
	if resp.StatusCode != http.StatusUnauthorized {
		return problem, "check the daemon's response and that DIBS_ADDR names the intended board"
	}
	dir := paths.DataDir()
	node, _ := os.ReadFile(filepath.Join(dir, "node_id")) // #nosec G304 -- operator's own configured directory
	local := boardconfig.BoardFingerprint(strings.TrimSpace(string(node)))
	remote := resp.Header.Get("X-Dibs-Board")
	decoded, err := hex.DecodeString(remote)
	if err != nil || len(decoded) != 16 {
		remote = ""
	}
	remote = strings.ToLower(remote)
	switch {
	case local != "" && remote != "" && local != remote:
		return problem + ": different board (address " + remote + "; data directory " + local + ")",
			"set DIBS_ADDR to this data directory's board address, or DIBS_DIR to the directory of the board at this address"
	case local != "" && local == remote:
		return problem + ": credential mismatch for board " + local,
			"the board identity matches " + dir + ", but its running credential differs from local.secret; " +
				"check which secret the running daemon loaded and whether this directory's secret changed"
	default:
		return problem + ": cannot identify the board",
			"this may be a different board or a credential mismatch; check DIBS_ADDR against the intended daemon's " +
				"listen address and DIBS_DIR against its data directory, then compare that directory's local.secret " +
				"with the credential the running daemon loaded"
	}
}

func daemonHTTPError(resp *http.Response) error {
	problem, hint := daemonRefusal(resp)
	return fmt.Errorf("%s; %s", problem, hint)
}

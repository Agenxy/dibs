// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package paths

import (
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"
)

// Three fixed slots, never a map of participant paths. A blocked spelling
// open must not become an unbounded log or one logger goroutine per request.
var spellingTimeoutLogged [3]atomic.Bool

func reportSpellingTimeout(path string) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	for i, folder := range []string{"Desktop", "Documents", "Downloads"} {
		rel, err := filepath.Rel(filepath.Join(home, folder), path)
		if err != nil || !filepath.IsLocal(rel) {
			continue
		}
		if spellingTimeoutLogged[i].CompareAndSwap(false, true) {
			// Logging must not extend the canonicalisation bound. At most one
			// log per protected root can remain waiting on an output writer.
			go slog.Warn("native path spelling timed out under a protected folder", "folder", folder,
				"hint", "Dibs kept the supplied spelling. Check System Settings → Privacy "+
					"& Security → Files & Folders → Dibs; OS consent may be pending")
		}
		return
	}
}

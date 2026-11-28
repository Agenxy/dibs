// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package sibling

import "os"

// Codex prefers the desktop app's runtime, which is the build its sessions
// actually use. Inspection and upgrade observation must resolve it identically.
func Codex() string {
	for _, path := range []string{
		"/Applications/ChatGPT.app/Contents/Resources/codex-cli/CodexCLI.app/Contents/MacOS/codex",
		"/Applications/ChatGPT.app/Contents/Resources/codex-cli/bin/codex",
		"/Applications/ChatGPT.app/Contents/Resources/codex",
	} {
		if st, err := os.Stat(path); err == nil && st.Mode().IsRegular() && st.Mode().Perm()&0o111 != 0 {
			return path
		}
	}
	return Find("codex")
}

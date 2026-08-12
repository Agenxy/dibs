// Package paths resolves the Dibs data directory in one place, so the
// daemon and CLI can never disagree about which instance they mean.
package paths

import (
	"os"
	"path/filepath"
)

// legacyDirs are data directories earlier versions used, newest first.
//
// `.agents` is not a name Dibs ever chose. The 0.0.3 rename replaced the word
// "lane" with "agent" throughout, and `~/.lanes` was swept up with the rest, so
// a tool called `dibs` shipped writing to a directory named for a generic noun
// that any of a dozen other tools could plausibly claim in a user's home.
//
// Keep reading them. A directory name is not the data, and there is no version
// of "your ledger is fine, it is just over there" that justifies losing a board.
//
// `.lanes` is deliberately NOT here. It can only be a 0.0.2 data directory, and
// 0.0.2 ledgers are the ones this version refuses on sight: inheriting one turns
// a clean first start into a refusal to boot, on any machine that ever ran the
// old release. Found on a real machine with a `.lanes` and no `.agents`, which
// is the exact shape that would have broken.
var legacyDirs = []string{".agents"}

// DataDir returns the active data directory.
//
// ~/.dibs, and never anywhere under Desktop, Documents or Downloads: those are
// TCC-protected on macOS, so a daemon reading them raises a folder-access
// prompt, and because TCC keys consent to the binary's identity, every rebuild
// invalidates the grant and asks again. A development instance belongs in
// DIBS_DIR, not in a path compiled into the binary.
func DataDir() string {
	dir, _ := Resolve()
	return dir
}

// Resolve returns the data directory and the legacy directory it came from, if
// any, so the daemon and `dibs doctor` can say which one they opened.
//
// The order is deliberate: an explicit DIBS_DIR always wins, then the current
// name, then an inherited one. Preferring `~/.dibs` when it exists means an
// install that has already moved never silently falls back to the old
// directory and forks its history in two.
func Resolve() (dir, inheritedFrom string) {
	if d := os.Getenv("DIBS_DIR"); d != "" {
		return d, ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".dibs", ""
	}
	current := filepath.Join(home, ".dibs")
	if exists(current) {
		return current, ""
	}
	for _, name := range legacyDirs {
		if old := filepath.Join(home, name); exists(old) {
			return old, old
		}
	}
	return current, ""
}

// ProjectStateDir returns where per-project monitor state (a nonce and a token)
// lives inside a checkout, with the same preference for an existing directory.
//
// This one sits in the USER's repository rather than their home, so the name
// matters more, not less: `.agents/` in a project root is a name several
// harnesses could reasonably claim, and Dibs writing there means two tools
// quietly sharing a directory neither one announced.
func ProjectStateDir(project string) string {
	current := filepath.Join(project, ".dibs")
	if exists(current) {
		return current
	}
	for _, name := range legacyDirs {
		if old := filepath.Join(project, name); exists(old) {
			return old
		}
	}
	return current
}

// exists reports whether a data directory is really there. A file of the same
// name is not one, and neither is an unreadable path: both should fall through
// to the current name and fail with an error about THAT, rather than reporting
// a legacy directory the caller cannot open either.
func exists(dir string) bool {
	fi, err := os.Stat(dir)
	return err == nil && fi.IsDir()
}

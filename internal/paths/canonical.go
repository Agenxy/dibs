package paths

import (
	"path/filepath"
)

// Canonical resolves p to the path the filesystem actually means, so that two
// agents naming the same directory two different ways are understood to be
// talking about the same directory.
//
// This is not cosmetic. Claims and the guard compare paths as STRINGS —
// deliberately, because that comparison has to be replayable from the ledger on
// any machine at any later time, and a state machine that stats the disk is not
// replayable. String comparison only works if the strings were canonicalised on
// the way in, which is what this does and why it lives here at the edge rather
// than in internal/core.
//
// The bug it exists for was measured, not imagined. On macOS /tmp, /var and
// /etc are symlinks into /private. The `lanes mcp-stdio` bridge records a
// lane's cwd from os.Getwd(), which returns the RESOLVED path
// (/private/var/folders/…/p), while a harness plugin guarding an edit passes
// the path the user typed (/var/folders/…/p). Those two strings do not
// overlap, so an exclusive claim taken by one agent silently failed to stop
// another — the guard fell open, exactly as designed, on a difference that was
// never real. Any repo reached through a symlink has the same problem, and
// symlinked checkouts are ordinary.
//
// It NEVER fails. An unresolvable path is returned lexically cleaned, because
// the callers are a claim and a permission check: refusing to answer is worse
// than answering on the name given.
func Canonical(p string) string {
	if p == "" {
		return ""
	}
	if !filepath.IsAbs(p) {
		if abs, err := filepath.Abs(p); err == nil {
			p = abs
		}
	}
	p = filepath.Clean(p)

	// EvalSymlinks requires every component to exist, and the guard is asked
	// about files that do not exist yet — `write` creating a new file is the
	// common case, and a check that skipped new files would be a hole exactly
	// where a rogue agent could drive through it. So resolve the deepest
	// ancestor that DOES exist and re-attach the rest, which is sound: the
	// missing components cannot themselves be symlinks.
	rest := ""
	cur := p
	for {
		if resolved, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(resolved, rest)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p // walked to the root without finding anything real
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}

package paths

import (
	"path"
	"path/filepath"
	"strings"

	"github.com/agenxy/dibs/internal/core"
)

// Canonical resolves p to the path the filesystem actually means, so that two
// agents naming the same directory two different ways are understood to be
// talking about the same directory.
//
// This is not cosmetic. Claims and the guard compare paths as STRINGS,
// deliberately, because that comparison has to be replayable from the ledger on
// any machine at any later time, and a state machine that stats the disk is not
// replayable. String comparison only works if the strings were canonicalised on
// the way in, which is what this does and why it lives here at the edge rather
// than in internal/core.
//
// The bug it exists for was measured, not imagined. On macOS /tmp, /var and
// /etc are symlinks into /private. The `dibs mcp-stdio` bridge records a
// agent's cwd from os.Getwd(), which returns the RESOLVED path
// (/private/var/folders/…/p), while a harness plugin guarding an edit passes
// the path the user typed (/var/folders/…/p). Those two strings do not
// overlap, so an exclusive claim taken by one agent silently failed to stop
// another: the guard fell open, exactly as designed, on a difference that was
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
	// about files that do not exist yet. `write` creating a new file is the
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

// Portable cleans a path that belongs to ANOTHER machine: lexically, and
// without touching this filesystem.
//
// Canonical resolves symlinks here, which is right for a path on this
// machine and wrong for one a remote bridge sent: /tmp on the hub is not
// /tmp on the caller, and what exists here says nothing about there. The
// remote bridge has already canonicalised on its own machine, and spelled
// its separators as `/` if it is a Windows machine (cmd/dibs, the one place
// that knows), so the only work left is cleaning. NO FOLD of `\` here: a
// backslash is an ordinary character in a unix filename, and folding it on
// the hub turned a remote /work/a\b into /work/a/b while the root beside it
// kept the backslash, so the checkout failed its own containment check.
// Round thirteen of the pre-release review.
func Portable(p string) string {
	if p == "" {
		return ""
	}
	// THE FOLD'S OWN FUNCTION, not a copy of it. This was eight hand-copied
	// lines with a comment on each side saying the two must stay identical:
	// a UNC root keeps its two slashes, because `//server/share/repo` names
	// a share on another machine and `/server/share/repo` names a local
	// directory, and a root recorded as one against paths cleaned to the
	// other is a prefix that never matches. The stated reason for copying
	// was that core may not import a package that touches the filesystem,
	// which is true and is the wrong direction: core imports nothing, so
	// this package can call it. Round forty-three of the pre-release review
	// wrote the rule; the consolidation before v0.0.8 removed the copy.
	return core.CleanPath(p)
}

// PortableBase is the last element of a Portable path, for a project label.
func PortableBase(p string) string {
	return path.Base(Portable(p))
}

// AbsElsewhere reports a path that is absolute on the machine it came
// from, though not on this one: a Windows drive path or a UNC share,
// spelled portably by the bridge there (Portable).
//
// ONE IMPLEMENTATION. A Unix hub asked its own filepath.IsAbs, so a
// Windows bridge's `C:/work/repo/file.go` was refused as relative before
// the remote-path handling that exists for exactly those callers could
// run (round thirty-six); round forty found the index upload asking the
// same question its own way and refusing the same callers. Two copies of
// one rule is this repository's most expensive recurring bug, so the rule
// lives here, below everything that needs it.
func AbsElsewhere(p string) bool {
	if strings.HasPrefix(p, "//") || strings.HasPrefix(p, `\\`) {
		return true // a UNC share
	}
	if len(p) < 3 || p[1] != ':' {
		return false
	}
	drive := p[0]
	if (drive < 'A' || drive > 'Z') && (drive < 'a' || drive > 'z') {
		return false
	}
	return p[2] == '/' || p[2] == '\\'
}

// Absolute reports a path that is absolute for whoever wrote it: rooted
// at a slash, absolute on this machine, or absolute on the machine it
// came from.
//
// THE LEADING SLASH IS NOT filepath.IsAbs ON WINDOWS, and a hub is not
// always a Unix one. The first cut of this asked filepath.IsAbs and the
// drive rule, which is the same answer on a Unix hub and refuses every
// Unix member's `/w/repo` on a Windows one: the Windows CI job caught it
// on the commit that introduced it. A path each side spells its own way
// is exactly what this predicate exists for, so it accepts both
// spellings on every host.
func Absolute(p string) bool {
	return strings.HasPrefix(p, "/") || filepath.IsAbs(p) || AbsElsewhere(p)
}

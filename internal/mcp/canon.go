package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/paths"
)

// canonPath canonicalizes a path at ingress (SPEC §9), so that the string
// comparison claims and the guard rely on is comparing the same directory when
// two agents name it two different ways.
//
// This used to resolve symlinks only when the ENTIRE path already existed, and
// that "only" was a hole the guard fell straight through. `claim` names a
// directory, which exists, so it was stored resolved. /private/var/…/p on
// macOS. `guard_path` names the FILE about to be written, which frequently does
// not exist yet, so resolution failed and it fell back to lexical cleaning,
// /var/…/p/f.txt. The two do not overlap as strings, so an exclusive claim
// silently failed to stop anybody, and the failure looked exactly like the
// deliberate fail-open. Verified against a live opencode agent: same file, same
// claim, deny when asked with the resolved name and allow with the alias.
//
// paths.Canonical resolves the deepest existing ancestor instead, which handles
// the not-yet-created file without giving up on the symlinked parents above it.
//
// Case-insensitive volumes and Unicode aliases remain documented caveats.
func canonPath(p string) string { return paths.Canonical(p) }

// callerPath is canonPath for a path on the CALLER's machine: resolved
// here when the caller is on this one, cleaned lexically and otherwise left
// alone when it is not.
//
// Resolving symlinks answers about this filesystem, and a remote caller's
// path names nothing on it: a Linux member's /tmp/repo/file, claimed through
// a macOS hub, became /private/tmp/repo/file, its recorded checkout root
// /tmp/repo no longer prefixed it, and the portable repository rule that
// exists for that agent had no relative path to fire on. guard_path and the
// lifecycle hooks compared the member's cwd the same wrong way. Registration
// had already learned this (resolveRemoteLocation); every other path the
// tools take now goes through the same decision. Found by the pre-release
// review, round five.
func callerPath(ctx context.Context, params json.RawMessage, p string) string {
	if p == "" {
		return ""
	}
	if remoteCaller(ctx, resolveHostID(ctx, params)) {
		return filepath.Clean(p)
	}
	return canonPath(p)
}

// mustBeAbsolute rejects a coordination path the caller never anchored.
//
// canonPath runs inside dibd, whose working directory is wherever it was
// started. `/` under launchd, and never the agent's. paths.Canonical therefore
// turns "internal/mcp" into "/internal/mcp": a directory that does not exist,
// that no other agent will ever name, and that overlaps nothing. claim answered
// granted:true and the board showed the claim, so the agent believed it held
// exclusive access to a directory it had not claimed at all: a coordination
// primitive reporting success for a no-op, which is the worst way this
// particular mechanism can fail.
//
// Refused rather than resolved against the agent's cwd, deliberately. The
// daemon does know the caller's cwd, so it COULD guess, but a claim is what
// other agents are asked to respect, and quietly rewriting one agent's shorthand
// into a different absolute path than the reader expects is how the guard's
// alias bug happened once already. Say what is wrong and let the caller name the
// place it means.
func mustBeAbsolute(field, p string) error {
	if p == "" || filepath.IsAbs(p) {
		return nil
	}
	return &core.Error{
		Code: "E_RELATIVE_PATH",
		Msg:  fmt.Sprintf("%s %q is relative", field, p),
		Hint: "pass an absolute path: the daemon's working directory is not yours, " +
			"so a relative one names a directory neither of you meant",
	}
}

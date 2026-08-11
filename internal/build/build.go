// Package build carries the version this binary was built from.
//
// One variable, because there were three. `internal/mcp` reported one to every
// agent on connect, `cmd/lanes` printed another, and `internal/web` put a third
// in the board's footer, and only the first two were stamped at link time. The
// third carried a comment saying "stamped by the build" that had never been
// true, so a released binary would have shown a development version on the most
// visible surface the project has, indefinitely, while `lanes version` in the
// same binary said something else.
//
// Nothing here prevents someone adding a fourth. What it does is make the
// release wire exactly one name, so a new surface that wants a version has an
// obvious thing to read rather than an obvious thing to declare.
package build

import (
	"runtime/debug"
	"strings"
)

// Version is what every surface reports. Overwritten at link time by
// -X github.com/agenxy/lanes/internal/build.Version=<tag>; anything else is
// worked out below from what the toolchain knows.
var Version = unstamped

// unstamped is the sentinel meaning "the linker told us nothing". It is not a
// version number, deliberately.
//
// It used to be "0.0.0-dev", which stated a release that exists: and, once
// v0.0.1 shipped, one that is OLDER than what people are running. A build from
// a tree four commits ahead of the release announced itself as 0.0.0 and read
// as stale. That is the same confusion `lanes version` was written to end: a
// daemon serving old code while everything else insisted the fix was in.
//
// So the fallback says what is true: this binary was not built from a release,
// and never a number that could be compared against one.
const unstamped = "devel"

func init() {
	if Version != unstamped {
		return // stamped by the release build; that value wins
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		Version = resolve(info)
	}
}

// resolve picks the most specific truthful version available.
//
// Split out from init and taking the whole BuildInfo so it can be tested: the
// interesting part is which source wins, and a precedence rule that nothing
// exercises is a rule that quietly inverts.
func resolve(info *debug.BuildInfo) string {
	// `go install pkg@v1.2.3` records the real module version. Best answer.
	switch v := info.Main.Version; v {
	case "", "(devel)":
		// Nothing from the module system; fall through to VCS below.
	default:
		return strings.TrimPrefix(v, "v")
	}

	// A local build. Go embeds the revision even when it has no version, and
	// that is exactly what somebody asking "is what is running what I last
	// built" needs: far more than a word saying "development".
	var rev string
	var dirty bool
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if rev == "" {
		return unstamped // go run, or -buildvcs=false, or no repository
	}
	if len(rev) > 12 {
		rev = rev[:12]
	}
	out := unstamped + "+" + rev
	if dirty {
		// Uncommitted changes mean the revision does not describe the binary.
		// Saying so is the difference between a useful answer and a wrong one.
		out += ".dirty"
	}
	return out
}

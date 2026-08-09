// Package build carries the version this binary was built from.
//
// One variable, because there were three. `internal/mcp` reported one to every
// agent on connect, `cmd/lanes` printed another, and `internal/web` put a third
// in the board's footer — and only the first two were stamped at link time. The
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
// -X github.com/agenxy/lanes/internal/build.Version=<tag>; the value below is
// what a `go build` from a working tree honestly is.
var Version = devVersion

// devVersion is the honest answer when nothing stamped the binary.
const devVersion = "0.0.0-dev"

// A `go install github.com/agenxy/lanes/cmd/lanes@latest` build carries no
// ldflags, so it reported 0.0.0-dev — a version string that says "somebody's
// working tree" for a binary the module proxy built from a tag. The README
// documents `go install` as an install route, so that is a supported build
// telling every surface, and every agent on connect, something false about
// itself.
//
// Go already knows: the module version is in the build info the toolchain
// embeds. Read it when the linker did not tell us, and leave a genuine local
// build saying -dev, which is what it is.
func init() {
	if info, ok := debug.ReadBuildInfo(); ok {
		Version = resolve(Version, info.Main.Version)
	}
}

// resolve picks the version to report, given whatever the linker stamped and
// whatever the module system knows. Split out from init so it can be tested:
// the interesting cases are all about which source wins, and a rule about
// precedence that nothing exercises is a rule that quietly inverts.
func resolve(stamped, moduleVersion string) string {
	if stamped != devVersion {
		return stamped // the release build said so; it wins
	}
	switch moduleVersion {
	case "", "(devel)":
		return devVersion // a local build inside the module, honestly
	default:
		return strings.TrimPrefix(moduleVersion, "v")
	}
}

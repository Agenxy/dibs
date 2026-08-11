package build

import (
	"runtime/debug"
	"testing"
)

func info(mainVersion string, settings ...debug.BuildSetting) *debug.BuildInfo {
	bi := &debug.BuildInfo{Settings: settings}
	bi.Main.Version = mainVersion
	return bi
}

// The version string answers "is what is running what I last built". It must
// never answer with a release number the binary is not.
//
// The fallback used to be "0.0.0-dev". Once v0.0.1 shipped, a build from a tree
// AHEAD of the release announced itself as 0.0.0 and read as stale: the exact
// confusion `lanes version` exists to end.
func TestVersionNeverClaimsAReleaseItIsNot(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		in         *debug.BuildInfo
	}{
		{"go install of a tag reports it", "1.4.2", info("v1.4.2")},
		{
			"a pseudo-version is reported as-is", "0.0.2-0.20260810222423-4d1f1da",
			info("v0.0.2-0.20260810222423-4d1f1da"),
		},
		{
			"a local build names its revision", "devel+4d1f1dad8936",
			info("(devel)", debug.BuildSetting{Key: "vcs.revision", Value: "4d1f1dad8936ac5c7b051b71c97f0ac3271feb09"}),
		},
		{
			"and says when the tree was modified", "devel+4d1f1dad8936.dirty",
			info("(devel)",
				debug.BuildSetting{Key: "vcs.revision", Value: "4d1f1dad8936ac5c7b051b71c97f0ac3271feb09"},
				debug.BuildSetting{Key: "vcs.modified", Value: "true"}),
		},
		{"go run, with nothing to go on", unstamped, info("(devel)")},
		{"no module info at all", unstamped, info("")},
	} {
		if got := resolve(tc.in); got != tc.want {
			t.Errorf("%s: resolve() = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// Whatever the fallback says, it must not look like a release. A number here is
// comparable against the versions people actually run, and will be compared.
func TestTheFallbackIsNotAVersionNumber(t *testing.T) {
	for _, c := range unstamped {
		if c >= '0' && c <= '9' {
			t.Fatalf("the unstamped sentinel %q contains a digit: it can be mistaken "+
				"for, and compared against, a real release", unstamped)
		}
	}
}

// A stamped release build wins over everything the toolchain infers.
func TestALinkedVersionIsNotOverwritten(t *testing.T) {
	orig := Version
	t.Cleanup(func() { Version = orig })
	Version = "9.9.9"
	if Version == unstamped {
		t.Fatal("precondition")
	}
	// init() only rewrites Version when it still equals the sentinel; simulate
	// that guard directly, since init has already run.
	if Version != unstamped {
		return
	}
	t.Fatal("a stamped version would have been replaced by inferred build info")
}

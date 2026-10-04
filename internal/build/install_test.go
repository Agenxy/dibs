package build

import (
	"runtime/debug"
	"testing"
)

func TestInstallStampRejectsOverridesOfTheInferredVersion(t *testing.T) {
	const pseudo = "0.0.12-0.20261004191805-4830e9556f7a"
	for _, tc := range []struct {
		name, module, flags string
		wantError           bool
	}{
		{"normal pseudo-version", "v" + pseudo, "", false},
		{"matching explicit stamp", "v" + pseudo, "-X " + versionSymbol + "=" + pseudo, false},
		{"release tag", "v0.0.11", "-X=" + versionSymbol + "=0.0.11", false},
		{"one-off devel override", "v" + pseudo, "-X=" + versionSymbol + "=devel+4830e9556f7a", true},
		{"false release", "v" + pseudo, "-s -w -X " + versionSymbol + "=0.0.12", true},
		{"single-quoted assignment", "v" + pseudo, "-X '" + versionSymbol + "=devel+bad'", true},
		{"double-quoted full field", "v" + pseudo, "\"-X=" + versionSymbol + "=devel+bad\"", true},
		{"different symbol", "v" + pseudo, "-X other.Version=devel", false},
		{"matching fallback", "(devel)", "-X=" + versionSymbol + "=devel", false},
		{"invented worktree revision", "(devel)", "-X=" + versionSymbol + "=devel+fullsha", true},
		{"unterminated quote", "v" + pseudo, "-X '" + versionSymbol + "=bad", true},
		{"missing assignment", "v" + pseudo, "-X", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := info(tc.module, debug.BuildSetting{Key: "-ldflags", Value: tc.flags})
			if err := CheckInstallStamp(in); (err != nil) != tc.wantError {
				t.Fatalf("CheckInstallStamp(%q) = %v, wantError %v", tc.flags, err, tc.wantError)
			}
		})
	}
}

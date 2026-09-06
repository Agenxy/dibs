package main

import "testing"

// The help said a bare run on an up-to-date install correctly does nothing,
// and the command stopped a serving daemon and restarted it onto the build
// it was already on. The decision is the part worth testing: when the
// daemon reports the installed build, there is nothing to do; a development
// build is never known to be the same code.
func TestAnUpToDateInstallIsNothingToDo(t *testing.T) {
	for _, tc := range []struct {
		name      string
		daemon    string
		installed string
		want      bool
	}{
		{"the daemon serves the installed build", "0.0.7-0.20260906-abc", "0.0.7-0.20260906-abc", true},
		{"the daemon serves an older build", "0.0.6", "0.0.7-0.20260906-abc", false},
		{"two development builds are not known to be the same code", "devel", "devel", false},
		{"a daemon that reports no version", "", "0.0.7", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := alreadyOn(buildInfo{Version: tc.daemon}, tc.installed); got != tc.want {
				t.Fatalf("alreadyOn(daemon %q, installed %q) = %v, want %v: %s", tc.daemon, tc.installed, got, tc.want,
					map[bool]string{true: "the fleet is restarted for no change", false: "a real upgrade is skipped"}[!tc.want])
			}
		})
	}
}

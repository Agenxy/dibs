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
		{"two dirty builds of one revision are not the same binary", "devel+abc123.dirty", "devel+abc123.dirty", false},
		{"a dirty build against a release", "0.0.7-0.20260906-abc", "0.0.7-0.20260906-abc.dirty", false},
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

// The first cut compared the daemon's version with this CLI's, and the
// replacement daemon is a binary of its own: a CLI and daemon on one build
// with a newer dibd installed beside them was told nothing to do, and the
// new daemon never ran. The decision reads the version the installed daemon
// reported for itself in its check line, never the CLI's.
func TestNothingToDoComparesTheInstalledDaemonNotTheCLI(t *testing.T) {
	line := []byte("checking that dibd can rebuild this board\n  ok: 0.0.8-0.20260907-newer replays 1991 record(s) to serial 1991 in 45ms (155 agent(s), 11 space(s))\n")
	if got := checkedVersion(line); got != "0.0.8-0.20260907-newer" {
		t.Fatalf("checkedVersion read %q from the check line", got)
	}
	if got := checkedVersion([]byte("something else entirely")); got != "" {
		t.Fatalf("checkedVersion invented %q from a line that is not a check report", got)
	}
	old := version
	version = "0.0.7-0.20260906-same"
	t.Cleanup(func() { version = old })
	p := &plan{serving: true, checked: "0.0.8-0.20260907-newer"}
	daemon := buildInfo{Version: "0.0.7-0.20260906-same"}
	if p.nothingToDo(daemon) {
		t.Fatal("the daemon serves the CLI's build and a newer dibd is installed: told nothing to do, " +
			"and the new daemon never runs")
	}
	p.checked = daemon.Version
	if !p.nothingToDo(daemon) {
		t.Fatal("the daemon serves the installed daemon's own build and the cutover would restart it for nothing")
	}
	p.unitWrong = true
	if p.nothingToDo(daemon) {
		t.Fatal("a unit that pins the wrong daemon is repaired by the cutover, and was skipped")
	}
}

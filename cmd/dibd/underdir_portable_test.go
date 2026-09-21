package main

import "testing"

// A portable path is contained with the portable separator.
//
// Both operands are made portable before the comparison, and the
// separator joining them was filepath.Separator: `\\` on Windows, so
// `C:/work/repo/pkg` was not beneath `C:/work/repo`. A Windows hub
// answered 403 to an index shipped from a subdirectory, and eviction did
// not count an agent in one as keeping the index alive.
//
// This test cannot fail on a unix host, where both separators are `/`:
// it pins the rule for the platform that has two of them, and the defect
// was found by reading rather than by running. Round forty-five of the
// pre-release review.
func TestPortablePathsAreContainedWithThePortableSeparator(t *testing.T) {
	for _, c := range []struct {
		p, dir string
		want   bool
	}{
		{`C:/work/repo/pkg`, `C:/work/repo`, true},
		{`C:/work/repo`, `C:/work/repo`, true},
		{`C:/work/repo-two/pkg`, `C:/work/repo`, false},
		{`//share/team/repo/pkg`, `//share/team/repo`, true},
		{`/w/repo/pkg`, `/w/repo`, true},
		{`/w/other/pkg`, `/w/repo`, false},
	} {
		if got := underDir(c.p, c.dir); got != c.want {
			t.Errorf("underDir(%q, %q) = %v, want %v", c.p, c.dir, got, c.want)
		}
	}
}

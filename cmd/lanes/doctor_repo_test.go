package main

import (
	"path/filepath"
	"testing"
)

// underDir decides whether `doctor` warns that you are working outside the
// indexed repository, so its edges matter more than its body.
//
// The warning exists because the daemon indexes ONE repository for the whole
// machine, and a board configured for one project while somebody works in
// another reports "matching ready (4577 files)" and then matches their
// declarations against a stranger's file layout. A reviewer was shown another
// project's paths as evidence of shared work with no way, from any Lanes
// surface, to discover why.
//
// A false warning is the expensive failure here: told they are in the wrong tree
// when they are not, somebody edits a working config. So the sibling-prefix case
// is pinned — /Users/x/Lanes-old must not read as inside /Users/x/Lanes.
func TestUnderDirDoesNotConfuseSiblingsForChildren(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "Lanes")
	for _, tc := range []struct {
		path string
		want bool
		why  string
	}{
		{repo, true, "the repository itself is inside itself"},
		{filepath.Join(repo, "internal", "core"), true, "a subdirectory is inside"},
		{
			filepath.Join(base, "Lanes-old"), false,
			"a sibling sharing a name prefix is NOT inside — this is the case a naive " +
				"strings.HasPrefix gets wrong, and it would warn somebody correctly " +
				"configured into breaking their config",
		},
		{base, false, "the parent is not inside the child"},
		{filepath.Join(base, "other-project"), false, "an unrelated sibling is not inside"},
	} {
		if got := underDir(tc.path, repo); got != tc.want {
			t.Errorf("underDir(%q, %q) = %v, want %v — %s", tc.path, repo, got, tc.want, tc.why)
		}
	}
}

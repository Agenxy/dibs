package paths

import "testing"

// A remote unix path keeps its backslashes. Portable folded `\` to `/` for
// every path a remote bridge sent, on the assumption that a backslash was
// a Windows separator; on a unix machine it is a character in a filename,
// and the fold turned /work/a\b into /work/a/b while the checkout root
// beside it kept the backslash, so the checkout failed its own containment
// check and the wake directory was wrong. The bridge on a Windows machine
// spells its own paths with `/` before sending. Round thirteen of the
// pre-release review.
func TestPortableKeepsABackslashInAUnixName(t *testing.T) {
	if got := Portable(`/work/a\b`); got != `/work/a\b` {
		t.Errorf("Portable(/work/a\\b) = %q, want the name unchanged: a backslash is a "+
			"character in a unix filename and the hub cannot tell whose path this is", got)
	}
	if got := Portable("/work//a/../b/"); got != "/work/b" {
		t.Errorf("Portable still cleans lexically: got %q", got)
	}
}

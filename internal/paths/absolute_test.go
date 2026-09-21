package paths

import "testing"

// Absolute answers the same on every host, because the two sides of a
// claim do not run the same one.
//
// This is the predicate a hub applies to a path a member wrote. Asking
// this machine's filepath.IsAbs plus the Windows spellings gives the
// right answer on a Unix hub and refuses every Unix member's `/w/repo`
// on a Windows one; the Windows job of the gate caught exactly that, on
// the commit that introduced it, through the index upload. Round forty
// of the pre-release review, on its own first fix.
func TestAbsoluteAcceptsEverySidesSpellingOnEveryHost(t *testing.T) {
	for _, p := range []string{
		"/w/repo", "/w/repo/file.go", // a Unix member's path, on any hub
		`C:/work/repo`, `C:\work\repo`, `d:/work/repo`, // a Windows member's
		"//share/team/repo", `\\share\team\repo`, // a UNC share
	} {
		if !Absolute(p) {
			t.Errorf("Absolute(%q) = false: that path is absolute for whoever wrote it, and a "+
				"hub that calls it relative refuses the caller it was written by", p)
		}
	}
	for _, p := range []string{"", "work/repo", "./work", "C:relative", "c:", "1:/work"} {
		if Absolute(p) {
			t.Errorf("Absolute(%q) = true, want false", p)
		}
	}
}

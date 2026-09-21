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

// A UNC root keeps both of its leading slashes.
//
// path.Clean collapses `//` to `/`, and that changes what the path
// names: `//server/share/repo` is a share on another machine,
// `/server/share/repo` is a local directory. A checkout root recorded as
// the first, with claims cleaned to the second, is a prefix that never
// matches, so no claim inside that checkout gets a repository-relative
// key and two hosts can hold one file exclusively. core.cleanPath says
// the same thing for the fold. Round forty-three of the pre-release
// review.
func TestPortableKeepsAUNCRoot(t *testing.T) {
	for in, want := range map[string]string{
		"//server/share/repo":       "//server/share/repo",
		"//server/share/repo/":      "//server/share/repo",
		"//server/share/repo/./pkg": "//server/share/repo/pkg",
		"///server/share":           "/server/share", // three is not a UNC root
		"/w/repo/":                  "/w/repo",
		"/w/repo/./pkg":             "/w/repo/pkg",
	} {
		if got := Portable(in); got != want {
			t.Errorf("Portable(%q) = %q, want %q", in, got, want)
		}
	}
}

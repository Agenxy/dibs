package main

import "testing"

// A Windows bridge sends its checkout spelled the way it sends its paths.
//
// The claim arguments went through portableSpelling and the repository
// metadata beside them did not, so the hub received a claim at
// `C:/work/repo/file.go` and a checkout root at `C:\work\repo`. The root
// is then not a prefix of the path, the claim records an empty
// repository-relative path, and the portable rule that exists so one
// project's two clones see each other's conflicts never fires. Round
// forty-one of the pre-release review.
func TestRepositoryMetadataTravelsSpelledLikeTheClaim(t *testing.T) {
	got := repoFields("windows", `C:\work\repo\.git`, "github.com/acme/api", "r1", `C:\work\repo`)
	if got["dir"] != "C:/work/repo/.git" || got["root"] != "C:/work/repo" {
		t.Fatalf("a Windows bridge sent dir=%q root=%q, which no claim path from it can be "+
			"matched against", got["dir"], got["root"])
	}
	// The remote and the root commits are fingerprints, not paths.
	if got["remote"] != "github.com/acme/api" || got["roots"] != "r1" {
		t.Fatalf("a fingerprint was rewritten as a path: %+v", got)
	}
	// And a unix bridge's backslash is an ordinary filename character.
	unix := repoFields("linux", `/w/re\po/.git`, "", "", `/w/re\po`)
	if unix["dir"] != `/w/re\po/.git` || unix["root"] != `/w/re\po` {
		t.Fatalf("a unix path was rewritten: %+v", unix)
	}
}

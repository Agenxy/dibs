package overlap

import "testing"

// An index shipped from a Windows checkout is accepted.
//
// Validate asked for a leading slash, so `C:/work/repo` was refused as
// relative and the upload answered 400: a Windows bridge could claim a
// path through a Unix hub (round thirty-six taught the hub that spelling)
// and could not ship the index that makes matching work for the tree that
// claim is in. The rule about what is absolute for whoever wrote it now
// lives in one place, paths.Absolute. Round forty of the pre-release
// review.
func TestAPayloadFromAWindowsCheckoutIsAccepted(t *testing.T) {
	payload := func(root string) *Payload {
		return &Payload{Root: root, Fingerprint: "fp-1", Files: []string{"internal/core/apply.go"}}
	}
	for _, root := range []string{`C:/work/repo`, `//share/team/repo`, "/w/repo"} {
		if err := payload(root).Validate(); err != nil {
			t.Errorf("a payload rooted at %q was refused: %v", root, err)
		}
	}
	// And what is not absolute anywhere is still refused.
	for _, root := range []string{"work/repo", "", "C:relative"} {
		if err := payload(root).Validate(); err == nil {
			t.Errorf("a payload rooted at %q was accepted: the root is what every file in it "+
				"is relative to, so a relative one indexes nothing findable", root)
		}
	}
}

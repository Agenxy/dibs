package overlap

import (
	"strings"
	"testing"
)

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

// A fingerprint is bounded, because it travels into the ledger.
//
// Validate checked that it was not empty and not that it was a digest,
// and the value is copied onto every declaration scored in that index:
// a 3 MiB fingerprint escaped past 18 MiB in the ledger line and put the
// ledger beyond what `dibs verify` can read back, so the append
// succeeded and verification then failed on that line and every later
// one. Round fifty-four of the pre-release review.
func TestAFingerprintIsBounded(t *testing.T) {
	payload := func(fp string) *Payload {
		return &Payload{Root: "/w/repo", Fingerprint: fp, Files: []string{"a.go"}}
	}
	if err := payload(strings.Repeat("<", 3<<20)).Validate(); err == nil {
		t.Fatal("a three-megabyte fingerprint was accepted: it lands in the ledger, escaped, " +
			"on every declaration scored in that index, and dibs verify cannot read the line back")
	}
	// An ordinary digest is not affected.
	if err := payload("sha256:" + strings.Repeat("a", 64)).Validate(); err != nil {
		t.Fatalf("an ordinary digest was refused: %v", err)
	}
}

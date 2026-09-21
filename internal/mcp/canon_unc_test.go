package mcp

import (
	"context"
	"encoding/json"
	"testing"
)

// A remote caller's UNC path keeps both of its leading slashes.
//
// The hub cleans a remote caller's path lexically, because resolving it
// against its own disk would name something else entirely (round five).
// It used filepath.Clean, which on a unix hub collapses `//` to `/`, so
// the claim arrived as `/server/share/repo/file.go` while the same
// agent's registration recorded the root as `//server/share/repo`: the
// root is not a prefix, nothing inside that checkout has a
// repository-relative key, and two hosts working one file in clones of
// one repository each keep an exclusive claim on it. Round forty-three
// taught the fold and paths.Portable, and this entry point kept cleaning
// the other way, which is why the changelog entry for it now says which
// layer it fixed. Round forty-four of the pre-release review.
func TestARemoteCallersUNCPathKeepsItsShare(t *testing.T) {
	ctx := context.WithValue(context.Background(), ownHostKey{}, "hub-node")
	params := json.RawMessage(`{"_meta":{"com.dibs/host":"machine-b"}}`)

	for in, want := range map[string]string{
		"//server/share/repo/file.go":   "//server/share/repo/file.go",
		"//server/share/repo/./file.go": "//server/share/repo/file.go",
		"//server/share/repo/":          "//server/share/repo",
		"/w/repo/file.go":               "/w/repo/file.go",
		"///server/share/repo":          "/server/share/repo",
	} {
		if got := callerPath(ctx, params, in); got != want {
			t.Errorf("callerPath(%q) = %q, want %q: a claim that loses a slash is outside "+
				"the checkout its own registration recorded", in, got, want)
		}
	}
}

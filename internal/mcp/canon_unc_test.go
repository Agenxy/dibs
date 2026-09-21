package mcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/core"
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

// The two bounds on a fingerprint are the same number.
//
// overlap sits below core and does not import it, so the constant
// exists twice: the upload refuses an oversized digest and the fold
// refuses one that arrives another way. Two copies of one number drift,
// and the failure is silent on whichever side is larger. Round
// fifty-four of the pre-release review.
func TestTheShipmentAndTheFoldBoundTheFingerprintAlike(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "overlap", "wire.go"))
	if err != nil {
		t.Fatal(err)
	}
	want := "const maxFingerprintBytes = " + strconv.Itoa(core.MaxFingerprintBytes)
	if !strings.Contains(string(src), want) {
		t.Fatalf("internal/overlap does not bound a fingerprint at %d, which is what "+
			"core.MaxFingerprintBytes says: one of the two accepts what the other refuses, "+
			"and the larger side decides what reaches the ledger", core.MaxFingerprintBytes)
	}
	// The same for a path, which is the other string a shipped index
	// puts into the ledger (round fifty-six).
	wantPath := "const maxPathBytes = " + strconv.Itoa(core.DefaultLimits().MaxPathBytes)
	if !strings.Contains(string(src), wantPath) {
		t.Fatalf("internal/overlap does not bound a shipped file path at %d, which is what "+
			"core's limits say", core.DefaultLimits().MaxPathBytes)
	}
}

// A resume records where the agent is NOW, not where it registered.
//
// resume takes a nonce and says nothing about location, so this
// recorded the machine and nothing else: an agent that registered on a
// desktop at /desktop/api and resumed on a laptop at /laptop/api kept
// the desktop's directory and repository identity. Its new claims had
// no repository-relative key, so two agents could hold one tracked file
// in clones of one project, and its wakes named a directory on the
// machine it had left. The bridge stamps its directory on every call;
// this reads it. Round fifty-four of the pre-release review.
func TestAResumeRecordsWhereTheAgentIsNow(t *testing.T) {
	ctx := context.WithValue(context.Background(), ownHostKey{}, "hub-node")
	params := json.RawMessage(`{"_meta":{"com.dibs/host":"laptop",` +
		`"com.dibs/repo":{"dir":"/laptop/api/.git","remote":"github.com/acme/api",` +
		`"roots":"r1","root":"/laptop/api","cwd":"/laptop/api/pkg"}}}`)

	info := resumeIdentity(ctx, params)
	if info == nil {
		t.Fatal("a resume from another machine recorded no identity at all")
	}
	if info.HostID != "laptop" {
		t.Errorf("host = %q, want the machine the resume came from", info.HostID)
	}
	if info.CWD != "/laptop/api/pkg" {
		t.Errorf("cwd = %q, want where the agent is now: the fold applies the location "+
			"group only when a cwd comes with it, so without this the row keeps the "+
			"directory it registered from", info.CWD)
	}
	if info.RepoRoot != "/laptop/api" || info.RepoRemote != "github.com/acme/api" {
		t.Errorf("repository identity = %q / %q, want this machine's checkout: without it "+
			"a claim here has no repository-relative key and stops colliding with the "+
			"same file in another clone", info.RepoRoot, info.RepoRemote)
	}
}

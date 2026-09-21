package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/agenxy/dibs/internal/overlap"
)

// A shipment names its root the way the registration did.
//
// The daemon refuses an index whose root is not the root it recorded for
// that agent. The bridge registered `C:/work/repo` and shipped
// `C:\work\repo`, so a Windows checkout was answered 403 on every upload
// and never got the index that is the only way its tree is matched at
// all. Round forty taught the hub to accept the portable spelling and
// left this half sending the native one; the changelog claimed the path
// worked. Round forty-two of the pre-release review.
func TestAShipmentNamesItsRootTheWayTheRegistrationDid(t *testing.T) {
	p := &overlap.Payload{
		Root: `C:\work\repo`, RepoDir: `C:\work\repo\.git`,
		RepoRemote: "github.com/acme/api", Fingerprint: "fp-1",
	}
	portablePayload("windows", p)
	if p.Root != "C:/work/repo" || p.RepoDir != "C:/work/repo/.git" {
		t.Fatalf("a Windows bridge ships root=%q repo_dir=%q, which is not what its "+
			"registration recorded, so the daemon refuses the upload", p.Root, p.RepoDir)
	}
	if p.RepoRemote != "github.com/acme/api" {
		t.Fatalf("a remote is a fingerprint, not a path, and was rewritten: %q", p.RepoRemote)
	}
	// A unix bridge's backslash is an ordinary filename character.
	u := &overlap.Payload{Root: `/w/re\po`, RepoDir: `/w/re\po/.git`}
	portablePayload("linux", u)
	if u.Root != `/w/re\po` || u.RepoDir != `/w/re\po/.git` {
		t.Fatalf("a unix path was rewritten: root=%q repo_dir=%q", u.Root, u.RepoDir)
	}
}

// The directories an agent declares are canonicalised like every other
// path it sends.
//
// `declare` was not in the table at all, so `dirs` went to the daemon
// exactly as the agent typed it: a Windows agent's `C:\repo\pkg` against
// a registered root of `C:/repo`, and a macOS agent's /tmp spelling
// against a resolved /private/tmp root. Neither can be made relative to
// its own checkout, so the strongest signal an agent gives about where it
// is writing matched nothing. Round forty-two of the pre-release review.
func TestDeclaredDirectoriesAreCanonicalisedLikeEveryOtherPath(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("no symlinks here: %v", err)
	}
	params := map[string]any{
		"name": "declare",
		"arguments": map[string]any{
			"dirs": []any{filepath.Join(link, "pkg"), "relative/stays"},
			"text": "some work",
		},
	}
	canonicalisePathArgs(params)

	args, _ := params["arguments"].(map[string]any)
	dirs, _ := args["dirs"].([]any)
	if len(dirs) != 2 {
		t.Fatalf("dirs came back as %v", args["dirs"])
	}
	// The temp directory is itself behind a symlink on macOS (/var ->
	// /private/var), so the expectation resolves too: this test is about
	// the argument being canonicalised at all, not about which spelling
	// this machine happens to use.
	resolved, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(resolved, "pkg")
	if got, _ := dirs[0].(string); got != want {
		t.Errorf("a declared directory was sent as %q, want %q: the daemon cannot make "+
			"that relative to the root this agent registered", got, want)
	}
	// A relative entry is the agent's own business: declare accepts one
	// against its root, and rewriting it here against this process's
	// working directory would name a directory nobody meant.
	if got, _ := dirs[1].(string); got != "relative/stays" {
		t.Errorf("a relative declared directory was rewritten to %q", got)
	}
}

// A path named on another agent's behalf is not this machine's to
// resolve.
//
// force_release with an `agent` quotes the path the BOARD shows, which
// is the holder's spelling on the holder's machine. The bridge resolved
// it here anyway, so a macOS coordinator releasing a Linux agent's
// /tmp/repo/file.go asked for /private/tmp/repo/file.go: the daemon
// answered E_NO_CLAIM and the claim it meant stayed exactly where it
// was. Round forty-six of the pre-release review.
func TestForceReleaseForAnotherAgentKeepsTheHoldersSpelling(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("no symlinks here: %v", err)
	}
	held := filepath.Join(link, "file.go")

	// Naming the holder: the path travels as the board spells it.
	params := map[string]any{
		"name":      "force_release",
		"arguments": map[string]any{"path": held, "agent": "far"},
	}
	canonicalisePathArgs(params)
	args, _ := params["arguments"].(map[string]any)
	if got, _ := args["path"].(string); got != held {
		t.Errorf("the bridge rewrote another agent's path to %q, want %q as the board shows it",
			got, held)
	}

	// Naming no holder, it is the caller's own claim and is resolved
	// here, which is what every other path argument gets.
	params = map[string]any{
		"name":      "force_release",
		"arguments": map[string]any{"path": held},
	}
	canonicalisePathArgs(params)
	args, _ = params["arguments"].(map[string]any)
	if got, _ := args["path"].(string); got == held {
		t.Errorf("a coordinator's own path was left unresolved (%q): the spelling it typed "+
			"has to meet the one the daemon recorded", got)
	}
}

// A bridge asks the daemon about its index with the spelling it shipped.
//
// The upload converts the root to its portable form and the daemon keys
// the status by that; this asked with the native one, so on Windows the
// two never matched. The bridge mined and uploaded the whole repository
// every five minutes for a shipment it had already made, and the
// daemon's fingerprint check threw the result away after the work and
// the transfer were done. Round fifty-two of the pre-release review, on
// round forty-two's own fix. Round forty-two converted what goes OUT
// and left what comes back being compared the other way.
func TestABridgeAsksAboutItsIndexWithTheSpellingItShipped(t *testing.T) {
	// Through the decision the ticker makes, with the bridge's own
	// native root: that is the call site the defect was at.
	st := matchStatusJSON{
		Remote:        []string{"C:/work/repo"},
		Supplied:      map[string]string{"C:/work/repo": "shipper"},
		SuppliedHosts: map[string]string{"C:/work/repo": "machine-b"},
		Host:          "hub-node",
	}
	const native = `C:\work\repo`
	if shouldShip(st, native, "machine-b", "windows") {
		t.Fatal("a Windows bridge would mine and upload this repository again for a " +
			"shipment the daemon already holds, every five minutes, for as long as it runs")
	}
	// And it still ships when the daemon really has nothing.
	empty := matchStatusJSON{Remote: []string{"C:/work/repo"}, Host: "hub-node"}
	if !shouldShip(empty, native, "machine-b", "windows") {
		t.Fatal("a Windows bridge stopped shipping an index the daemon has never received")
	}
	// The native spelling is exactly what did not match before: asked
	// directly, the daemon's answer does not cover it.
	if suppliedFor(st, native, "machine-b") {
		t.Fatal("the native spelling matched after all: this test is not measuring the " +
			"difference it was written for")
	}
}

// A hook call reaches the daemon with a working directory even when the
// harness states none.
//
// `guard_path`'s cwd is how the daemon finds the agent when the harness's
// session id is not the one the agent registered under, and the opencode
// plugin used to measure and canonicalise it itself. It is a transport now
// and states nothing about this machine, deliberately, so the rule moved to
// the bridge: this process is spawned by the harness and inherits its
// directory, which makes it the one place that knows the answer for every
// harness at once. A rule dropped by one side and not picked up by the
// other is the shape this consolidation exists to prevent, so it is tested
// rather than assumed.
//
// Measured against the commit before the fix rather than asserted: the same
// pipeline there (canonicalisePathArgs alone, since supplyWorkingDirectory
// did not exist) leaves `guard_path` with no cwd key at all, which is the
// first check below.
func TestAHookCallCarriesAWorkingDirectoryTheHarnessDidNotState(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"guard_path", "hook_poll", "hook_session", "hook_blocked"} {
		params := map[string]any{
			"name":      tool,
			"arguments": map[string]any{"session_id": "s", "path": filepath.Join(wd, "x.go")},
		}
		supplyWorkingDirectory(params)
		canonicalisePathArgs(params)
		args, _ := params["arguments"].(map[string]any)
		got, _ := args["cwd"].(string)
		if got == "" {
			t.Errorf("%s reached the daemon with no cwd: the lookup that finds an agent by "+
				"its directory has nothing to match, and a guard that resolves nobody allows "+
				"the edit", tool)
			continue
		}
		if want := canonicaliseOne(wd); got != want {
			t.Errorf("%s carried cwd %q, want %q as the daemon compares it", tool, got, want)
		}
	}

	// What the caller states still wins: `dibs hook` knows Claude Code's
	// directory, which is not this process's when a hook runs elsewhere.
	stated := map[string]any{
		"name":      "guard_path",
		"arguments": map[string]any{"session_id": "s", "cwd": t.TempDir()},
	}
	before, _ := stated["arguments"].(map[string]any)
	said, _ := before["cwd"].(string)
	supplyWorkingDirectory(stated)
	if got, _ := before["cwd"].(string); got != said {
		t.Errorf("the bridge overwrote a cwd the caller stated: %q became %q", said, got)
	}

	// And a tool whose cwd is not the harness's directory is left alone:
	// register takes its own from the harness's sidecar where there is one.
	reg := map[string]any{"name": "register", "arguments": map[string]any{}}
	supplyWorkingDirectory(reg)
	args, _ := reg["arguments"].(map[string]any)
	if _, filled := args["cwd"]; filled {
		t.Error("register's cwd was filled from this process: the sidecar's answer is the " +
			"harness's own directory and is the better one")
	}
}

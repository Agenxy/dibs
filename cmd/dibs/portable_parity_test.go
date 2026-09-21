package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/overlap"
)

// Every harness spells a Windows path the same way when it sends it.
//
// The rule is one sentence: on Windows, `\` becomes `/` before a path
// leaves this machine; everywhere else a backslash is an ordinary filename
// character and nothing is touched. The bridge has applied it since round
// thirteen and the two TypeScript plugins did not, so a Windows agent on
// opencode or pi claimed `C:\repo\file.go` while its own registration had
// recorded the root as `C:/repo`. A unix hub keeps both spellings exactly
// as they arrive, so the claim was no longer inside the checkout it names:
// no repository-relative key, no collision, and an exclusive claim that
// protects nothing. Round forty-two of the pre-release review.
//
// One table, run through all three implementations, for the reason the
// stamp parity test gives: a rule that exists three times drifts, and the
// symptom is silent on the two copies nobody runs.
func TestEveryHarnessSpellsAWindowsPathTheSameWay(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("bun is not installed; the TypeScript halves cannot be run")
	}

	cases := []string{
		`C:\work\repo\internal\core\apply.go`,
		`C:/work/repo/internal/core/apply.go`,
		`\\share\team\repo\file.go`,
		`/w/repo/file.go`,
		`/w/re\po/file.go`, // a unix filename that contains a backslash
		``,
	}

	// The Go answer, from the bridge's own function.
	want := make([]string, len(cases))
	for i, c := range cases {
		want[i] = spellFor("windows", c)
	}

	for _, plugin := range []string{"opencode", "pi"} {
		path := filepath.Join("..", "..", "plugins", plugin, "dibs.ts")
		src, err := os.ReadFile(path) // #nosec G304 -- a file in this repository
		if err != nil {
			t.Fatalf("reading the %s plugin: %v", plugin, err)
		}
		const marker = "function portable(p: string, plat: string = process.platform): string {"
		start := strings.Index(string(src), marker)
		if start < 0 {
			t.Fatalf("plugins/%s/dibs.ts no longer defines portable(): either it was renamed, "+
				"in which case this test must follow it, or the conversion was removed, in "+
				"which case a Windows agent on that harness silently claims paths its own "+
				"registration cannot be matched against", plugin)
		}
		end := strings.Index(string(src)[start:], "\n}\n")
		if end < 0 {
			t.Fatalf("could not find the end of portable() in plugins/%s/dibs.ts", plugin)
		}
		fn := string(src)[start : start+end+3]

		table, err := json.Marshal(cases)
		if err != nil {
			t.Fatal(err)
		}
		script := fn + "\nconst cases = " + string(table) +
			" as string[]\nconsole.log(JSON.stringify(cases.map((c) => portable(c, \"win32\"))))\n"

		dir := t.TempDir()
		file := filepath.Join(dir, "parity.ts")
		if err := os.WriteFile(file, []byte(script), 0o600); err != nil {
			t.Fatal(err)
		}
		out, err := exec.Command(bun, "run", file).Output() // #nosec G204 -- paths this test created
		if err != nil {
			t.Fatalf("running the %s plugin's rule: %v\n%s", plugin, err, out)
		}
		var got []string
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("the %s plugin did not return a string list: %v\n%s", plugin, err, out)
		}
		if len(got) != len(want) {
			t.Fatalf("%s returned %d answers for %d cases", plugin, len(got), len(want))
		}
		for i, c := range cases {
			if got[i] != want[i] {
				t.Errorf("the bridge and the %s plugin disagree about %q: Go sends %q, "+
					"the plugin sends %q. One of them is naming a path the hub cannot "+
					"match against what the other registered.", plugin, c, want[i], got[i])
			}
		}
	}
}

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

// The bridge and the pi plugin resolve the same path arguments.
//
// Both sit between an agent and the daemon, and both have to turn what
// the agent typed into what the daemon compares against: a symlinked
// spelling, a Windows separator, a relative entry left alone. They kept
// separate lists, and pi's was missing `declare.dirs`, `register.cwd`
// and `update.cwd`, so an agent on pi declared directories the hub could
// not place inside its own checkout. One list is not possible across two
// languages; agreeing on the same pairs is, and this is what catches the
// next one being added to only one side. Round forty-three of the
// pre-release review.
func TestTheBridgeAndThePiPluginResolveTheSameArguments(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "plugins", "pi", "dibs.ts"))
	if err != nil {
		t.Fatal(err)
	}
	const marker = "const PATH_ARGS: Record<string, string[]> = {"
	start := strings.Index(string(src), marker)
	if start < 0 {
		t.Fatal("plugins/pi/dibs.ts no longer defines PATH_ARGS")
	}
	end := strings.Index(string(src)[start:], "\n}\n")
	if end < 0 {
		t.Fatal("could not find the end of PATH_ARGS")
	}
	block := string(src)[start : start+end]

	// Every tool the Go bridge resolves must appear in pi's table with
	// the same argument names.
	for tool, keys := range pathArgs {
		line := regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(tool) + `:\s*\[([^\]]*)\]`).
			FindStringSubmatch(block)
		if line == nil {
			t.Errorf("the pi plugin does not resolve %q, which the bridge does: an agent on "+
				"pi sends that argument as typed, and the daemon compares it against a "+
				"spelling it resolved", tool)
			continue
		}
		for _, k := range keys {
			if !strings.Contains(line[1], `"`+k+`"`) {
				t.Errorf("the pi plugin resolves %q but not its %q argument", tool, k)
			}
		}
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

// Pi keeps another agent's path as the board spells it, exactly as the
// bridge does.
//
// Round forty-six gave the Go bridge and the hub the exception and left
// pi resolving every listed path argument, including a force_release
// that names somebody else's claim: a Linux holder's /tmp/repo/file.go
// became this Mac's /private/tmp/repo/file.go, and a Windows holder's
// C:/repo/file.go picked up this machine's working directory as a
// prefix. Both are paths no claim on the board matches. One rule, both
// implementations, one table. Round forty-seven of the pre-release
// review.
func TestPiKeepsAnotherAgentsForceReleasePath(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("bun is not installed; the TypeScript half cannot be run")
	}
	src, err := os.ReadFile(filepath.Join("..", "..", "plugins", "pi", "dibs.ts"))
	if err != nil {
		t.Fatal(err)
	}
	// The predicate the plugin applies, lifted from the shipped file so a
	// change there is what gets tested.
	const marker = `const forAnother = `
	start := strings.Index(string(src), marker)
	if start < 0 {
		t.Fatal("plugins/pi/dibs.ts no longer decides whether a path belongs to another " +
			"agent: a force_release naming a holder is being resolved on this machine again, " +
			"and the daemon will answer E_NO_CLAIM while the real claim stays")
	}
	end := strings.Index(string(src)[start:], "\n")
	expr := strings.TrimSpace(string(src)[start+len(marker) : start+end])

	script := "const cases = [\n" +
		`  { name: "force_release", args: { path: "/tmp/repo/file.go", agent: "far" } },` + "\n" +
		`  { name: "force_release", args: { path: "/tmp/repo/file.go" } },` + "\n" +
		`  { name: "claim", args: { path: "/tmp/repo/file.go" } },` + "\n" +
		"]\nconst out = cases.map((c) => { const p = { name: c.name, arguments: c.args } as Record<string, unknown>\n" +
		"  const args0 = (p[\"arguments\"] ?? {}) as Record<string, unknown>\n" +
		"  return " + expr + "\n})\nconsole.log(JSON.stringify(out))\n"

	dir := t.TempDir()
	file := filepath.Join(dir, "another.ts")
	if err := os.WriteFile(file, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(bun, "run", file).Output() // #nosec G204 -- paths this test created
	if err != nil {
		t.Fatalf("running the plugin's rule: %v\n%s", err, out)
	}
	var got []bool
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("the plugin's rule did not return booleans: %v\n%s", err, out)
	}
	want := []bool{true, false, false}
	for i := range want {
		if i >= len(got) || got[i] != want[i] {
			t.Fatalf("pi's rule answered %v, want %v: case 0 is another agent's claim and "+
				"must be left alone; the other two are this machine's own paths", got, want)
		}
	}
}

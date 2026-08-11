package mcp

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Whether two agents are in one project is decided from facts recorded at
// registration, and those facts come from Git. Synthetic AgentInfo values in
// internal/core prove the DECISION and say nothing about the resolver that
// feeds it, which is precisely the gap that let several rounds of defects
// through: every unit case passed while a shallow clone, a subtree host and a
// rename redirect were each being resolved wrongly one layer down. A reviewer
// put it exactly right, that the synthetic cases supply a shared root and
// therefore never exercise the fallback.
//
// So this builds real repositories, registers real agents through the real
// server, and asserts on the warning an agent would actually receive. Every
// case below was answered incorrectly at some point.
func TestRefScopingAcrossRealRepositories(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is unavailable")
	}
	root := t.TempDir()
	g := &gitFixtures{t: t, git: git, root: root}

	upstream := g.repo("upstream", "https://github.com/acme/upstream.git")
	g.commit(upstream, "second.txt")

	for _, tc := range []struct {
		what      string
		build     func() (a, b string)
		wantAlarm bool
		why       string
	}{
		{
			what: "two agents in one checkout",
			build: func() (string, string) {
				deep := filepath.Join(upstream, "internal", "store")
				if err := os.MkdirAll(deep, 0o750); err != nil {
					t.Fatal(err)
				}
				return upstream, deep
			},
			wantAlarm: true,
			why:       "one repository, and catching this is the whole reason the signal exists",
		},
		{
			what: "two unrelated projects",
			build: func() (string, string) {
				return g.repo("alpha", "https://github.com/acme/alpha.git"),
					g.repo("beta", "https://github.com/acme/beta.git")
			},
			wantAlarm: false,
			why:       "different upstreams, no commit in common: issue 42 is just a number they share",
		},
		{
			what: "a clone whose origin was removed",
			build: func() (string, string) {
				b := g.clone(upstream, "unremoted")
				g.run(b, "remote", "remove", "origin")
				return g.clone(upstream, "kept"), b
			},
			wantAlarm: true,
			why:       "a clone keeps its history when it loses its remote",
		},
		{
			what: "remotes differing only by letter case",
			build: func() (string, string) {
				a, b := g.clone(upstream, "case-a"), g.clone(upstream, "case-b")
				g.run(a, "remote", "set-url", "origin", "https://github.com/Acme/Upstream.git")
				g.run(b, "remote", "set-url", "origin", "https://github.com/acme/upstream.git")
				return a, b
			},
			wantAlarm: true,
			why:       "forges serve those as one repository; git ls-remote returns one HEAD for both",
		},
		{
			what: "one repository, one clone holding a stale url after a rename",
			build: func() (string, string) {
				a, b := g.clone(upstream, "rename-old"), g.clone(upstream, "rename-new")
				g.run(a, "remote", "set-url", "origin", "https://github.com/acme/upstream-old.git")
				return a, b
			},
			wantAlarm: true,
			why:       "a renamed repository keeps serving its old path, so a stale url is not another project",
		},
		{
			what: "clones of one upstream with no commit in common",
			build: func() (string, string) {
				branch := g.head(upstream)
				g.run(upstream, "checkout", "-q", "--orphan", "docs")
				g.commit(upstream, "docs.txt")
				g.run(upstream, "checkout", "-q", branch)
				return g.clone(upstream, "orphan-main", "--single-branch", "--branch", branch),
					g.clone(upstream, "orphan-docs", "--single-branch", "--branch", "docs")
			},
			wantAlarm: true,
			why:       "one configured upstream, and history may legitimately diverge inside a project",
		},
		{
			what: "two projects that vendored one dependency by subtree",
			build: func() (string, string) {
				vendor := g.repo("vendor", "")
				g.commit(vendor, "lib.txt")
				a := g.repo("host-a", "https://github.com/acme/host-a.git")
				b := g.repo("host-b", "https://github.com/acme/host-b.git")
				for _, host := range []string{a, b} {
					g.run(host, "subtree", "add", "--prefix=vendor",
						"file://"+vendor, g.head(vendor), "-q")
				}
				return a, b
			},
			wantAlarm: false,
			why:       "an imported dependency is not a shared project; each host keeps its own root",
		},
		{
			what: "shallow clones at different depths, both origins removed",
			build: func() (string, string) {
				a := g.clone(upstream, "shallow-1", "--depth", "1")
				b := g.clone(upstream, "shallow-2", "--depth", "2")
				g.run(a, "remote", "remove", "origin")
				g.run(b, "remote", "remove", "origin")
				return a, b
			},
			wantAlarm: true,
			why:       "a shallow clone has no root to compare, and not knowing must not read as different",
		},
		{
			what: "a history rewritten locally with git replace",
			build: func() (string, string) {
				a, b := g.clone(upstream, "graft-a"), g.clone(upstream, "graft-b")
				g.run(b, "replace", "--graft", "HEAD")
				return a, b
			},
			wantAlarm: true,
			why:       "a replacement is a local view of the objects, not a different history",
		},
	} {
		t.Run(tc.what, func(t *testing.T) {
			a, b := tc.build()
			srv, _ := newServer(t)
			const ref = "issue:42"
			mustDeclare(t, srv, "agent-a", a, ref)
			res := mustDeclare(t, srv, "agent-b", b, ref)

			blob, err := json.Marshal(res)
			if err != nil {
				t.Fatal(err)
			}
			got := strings.Contains(string(blob), `"signal":"same-objective"`)
			if got != tc.wantAlarm {
				verb := "did not warn"
				if got {
					verb = "warned"
				}
				t.Errorf("%s: the second agent %s about a shared ref, want alarm=%v\n  %s\n  got: %s",
					tc.what, verb, tc.wantAlarm, tc.why, blob)
			}
		})
	}
}

// mustDeclare registers an agent from a working directory and declares one ref.
// Every step is checked: a probe that quietly fails to register reports "no
// warning" and reads as a pass, which is how a check comes to prove nothing.
func mustDeclare(t *testing.T, srv *httptest.Server, name, cwd, ref string) map[string]any {
	t.Helper()
	out := toolCall(t, srv, "register_lane", map[string]any{
		"name": name, "cwd": cwd,
		"nonce": "n-" + name + "-0123456789abcdef0123",
	})
	token, _ := out["token"].(string)
	if token == "" {
		t.Fatalf("%s could not register from %s: %v", name, cwd, out)
	}
	toolCall(t, srv, "ack_board", map[string]any{"token": token})
	return toolCall(t, srv, "set_slot", map[string]any{
		"token": token, "text": "working on " + ref, "refs": []string{ref},
	})
}

// gitFixtures builds the repositories these cases need. Each helper fails the
// test rather than returning an error, because a fixture that half-built would
// otherwise be measured as a result.
type gitFixtures struct {
	t    *testing.T
	git  string
	root string
}

func (g *gitFixtures) run(dir string, args ...string) {
	g.t.Helper()
	cmd := exec.Command(g.git, append([]string{"-C", dir}, args...)...) // #nosec G204
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid",
		"GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid")
	if out, err := cmd.CombinedOutput(); err != nil {
		g.t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
}

// head is the checked-out branch name, read rather than assumed: `git init`
// names it from configuration, so hardcoding "main" makes the fixtures depend on
// whoever is running them.
func (g *gitFixtures) head(dir string) string {
	g.t.Helper()
	cmd := exec.Command(g.git, "-C", dir, "symbolic-ref", "--short", "HEAD") // #nosec G204
	out, err := cmd.Output()
	if err != nil {
		g.t.Fatalf("reading the branch of %s: %v", dir, err)
	}
	return strings.TrimSpace(string(out))
}

// commit writes a file whose content is its own name, so each repository has
// distinct history. Empty commits made in one second by one author hash
// identically, which once made four unrelated fixtures look like one project.
func (g *gitFixtures) commit(dir, name string) {
	g.t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(dir+"/"+name+"\n"), 0o600); err != nil {
		g.t.Fatal(err)
	}
	g.run(dir, "add", name)
	g.run(dir, "commit", "-q", "-m", "add "+name)
}

func (g *gitFixtures) repo(name, remote string) string {
	g.t.Helper()
	dir := filepath.Join(g.root, name)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		g.t.Fatal(err)
	}
	g.run(dir, "init", "-q")
	g.commit(dir, "README.md")
	if remote != "" {
		g.run(dir, "remote", "add", "origin", remote)
	}
	return dir
}

func (g *gitFixtures) clone(src, name string, extra ...string) string {
	g.t.Helper()
	dst := filepath.Join(g.root, name)
	args := append([]string{"clone", "-q"}, extra...)
	g.run(g.root, append(args, "file://"+src, dst)...)
	return dst
}

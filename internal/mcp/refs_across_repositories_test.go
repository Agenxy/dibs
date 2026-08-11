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
			mustDeclare(t, srv, "agent-a", a)
			res := mustDeclare(t, srv, "agent-b", b)

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
func mustDeclare(t *testing.T, srv *httptest.Server, name, cwd string) map[string]any {
	const ref = "issue:42" // one ref throughout: what varies here is the repositories
	t.Helper()
	out := toolCall(t, srv, "register", map[string]any{
		"name": name, "cwd": cwd,
		"nonce": "n-" + name + "-0123456789abcdef0123",
	})
	token, _ := out["token"].(string)
	if token == "" {
		t.Fatalf("%s could not register from %s: %v", name, cwd, out)
	}
	toolCall(t, srv, "check_in", map[string]any{"token": token})
	return toolCall(t, srv, "declare", map[string]any{
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

// A daemon runs for weeks. `rm -rf project && git clone something-else project`
// is an ordinary thing to do in that time, and repository identity is memoised
// by path with no expiry, so the entry went on describing the repository that
// used to be there. Both directions were wrong: the new project's agents were
// compared against the old project's remote and roots.
//
// Reported by a review that reused a checkout path and drove both directions
// through the real server, which is the only way to see it: the resolver is
// correct in isolation and the cache is what serves the stale answer.
func TestIdentityIsRereadWhenACheckoutPathIsReused(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is unavailable")
	}

	t.Run("a collision at a reused path is not missed", func(t *testing.T) {
		g := &gitFixtures{t: t, git: git, root: t.TempDir()}
		upstream := g.repo("upstream", "https://github.com/acme/upstream.git")
		sibling := g.clone(upstream, "sibling")
		reused := g.repo("reused", "https://github.com/acme/unrelated.git")

		srv, _ := newServer(t)
		primeIdentityCache(t, srv, "primer", reused)
		mustDeclare(t, srv, "agent-a", sibling)

		// Same path, different repository: now a clone of the same upstream.
		if err := os.RemoveAll(reused); err != nil {
			t.Fatal(err)
		}
		reused = g.clone(upstream, "reused")

		if !warnedAboutSharedObjective(t, mustDeclare(t, srv, "agent-b", reused)) {
			t.Error("two agents in one repository were not warned, because the identity " +
				"cached for that path still described the repository deleted from it")
		}
	})

	t.Run("a stranger at a reused path does not raise one", func(t *testing.T) {
		g := &gitFixtures{t: t, git: git, root: t.TempDir()}
		upstream := g.repo("upstream", "https://github.com/acme/upstream.git")
		sibling := g.clone(upstream, "sibling")
		reused := g.clone(upstream, "reused")

		srv, _ := newServer(t)
		primeIdentityCache(t, srv, "primer", reused)
		mustDeclare(t, srv, "agent-a", sibling)

		if err := os.RemoveAll(reused); err != nil {
			t.Fatal(err)
		}
		reused = g.repo("reused", "https://github.com/acme/unrelated.git")

		if warnedAboutSharedObjective(t, mustDeclare(t, srv, "agent-b", reused)) {
			t.Error("an unrelated project was reported as duplicating work, because the " +
				"identity cached for that path still described the clone deleted from it")
		}
	})
}

// primeIdentityCache registers an agent purely so the daemon resolves and
// memoises that directory. Without this the second registration would be the
// first time the path is seen and there would be nothing stale to catch.
func primeIdentityCache(t *testing.T, srv *httptest.Server, name, cwd string) {
	t.Helper()
	out := toolCall(t, srv, "register", map[string]any{
		"name": name, "cwd": cwd, "nonce": "n-" + name + "-0123456789abcdef0123",
	})
	if token, _ := out["token"].(string); token == "" {
		t.Fatalf("priming registration from %s failed: %v", cwd, out)
	}
}

func warnedAboutSharedObjective(t *testing.T, result map[string]any) bool {
	t.Helper()
	blob, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Contains(string(blob), `"signal":"same-objective"`)
}

// The other half of the same bug, and the one I saw coming and left. Cached
// identity was revalidated only when it said "this is a repository", so a
// directory observed BEFORE `git init` was remembered as not-a-repository for
// the life of the daemon.
//
// Asserted on the recorded FACTS rather than on a warning, and that distinction
// is the point. A stale negative makes the answer unknown, and unknown warns, so
// a collision test passes with the bug present and proves nothing. What is
// actually broken is that the agent is filed as being nowhere: no project on the
// board, and no identity for anything else to reason with.
//
// Predicting a hole and not closing it is a slower way of shipping it.
func TestADirectoryThatBecomesARepositoryIsNoticed(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is unavailable")
	}
	g := &gitFixtures{t: t, git: git, root: t.TempDir()}

	later := filepath.Join(g.root, "becomes-a-repo")
	if err := os.MkdirAll(later, 0o750); err != nil {
		t.Fatal(err)
	}
	srv, _ := newServer(t)
	primeIdentityCache(t, srv, "primer", later)

	g.run(later, "init", "-q")
	g.run(later, "remote", "add", "origin", "https://github.com/acme/upstream.git")
	g.commit(later, "README.md")

	out := toolCall(t, srv, "register", map[string]any{
		"name": "after-init", "cwd": later,
		"nonce": "n-after-init-0123456789abcdef0123",
	})
	token, _ := out["token"].(string)
	if token == "" {
		t.Fatalf("registration failed: %v", out)
	}
	board, err := json.Marshal(toolCall(t, srv, "check_in", map[string]any{"token": token}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(board), `"project":"becomes-a-repo"`) {
		t.Errorf("an agent in a directory that became a repository is still filed as "+
			"being nowhere: the daemon is remembering that the path used to be nothing.\n%s",
			board)
	}
}

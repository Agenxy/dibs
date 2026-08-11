package paths

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSameRepoRecognisesLinkedWorktrees(t *testing.T) {
	git := requireGit(t)
	root := t.TempDir()
	repo := filepath.Join(root, "main")
	worktree := filepath.Join(root, "linked")
	initRepo(t, git, repo)
	runGit(t, git, repo, "worktree", "add", "-b", "linked-worktree", worktree)

	mainID := Identify(repo)
	linkedID := Identify(worktree)
	if same, known := SameRepo(mainID, linkedID); !same || !known {
		t.Fatalf("SameRepo(main, linked worktree) = (%v, %v), want (true, true)", same, known)
	}
	if mainID.WorktreeID == linkedID.WorktreeID {
		t.Fatalf("linked worktrees have the same worktree ID %q", mainID.WorktreeID)
	}
	if mainID.WorktreeID != realPath(t, repo) {
		t.Errorf("main WorktreeID = %q, want %q", mainID.WorktreeID, realPath(t, repo))
	}
	if linkedID.WorktreeID != realPath(t, worktree) {
		t.Errorf("linked WorktreeID = %q, want %q", linkedID.WorktreeID, realPath(t, worktree))
	}
}

func TestSameRepoNormalisesPrimaryRemoteAcrossClones(t *testing.T) {
	git := requireGit(t)
	root := t.TempDir()
	source := filepath.Join(root, "source")
	initRepo(t, git, source)

	var clones []string
	for _, name := range []string{"scp", "https", "ssh"} {
		clone := filepath.Join(root, name)
		runGit(t, git, root, "clone", source, clone)
		clones = append(clones, clone)
	}
	runGit(t, git, clones[0], "remote", "set-url", "origin", "git@GitHub.com:agenxy/lanes.git")
	runGit(t, git, clones[1], "remote", "set-url", "origin", "https://github.com/agenxy/lanes")
	runGit(t, git, clones[2], "remote", "set-url", "origin", "ssh://git@github.com/agenxy/lanes.git")

	for i := 1; i < len(clones); i++ {
		if same, known := SameRepo(Identify(clones[0]), Identify(clones[i])); !same || !known {
			t.Errorf("SameRepo(%q, %q) = (%v, %v), want (true, true)", clones[0], clones[i], same, known)
		}
	}
}

func TestSameRepoReportsDifferentNormalisedRemotes(t *testing.T) {
	git := requireGit(t)
	root := t.TempDir()
	first := filepath.Join(root, "first")
	second := filepath.Join(root, "second")
	initRepo(t, git, first)
	initRepo(t, git, second)
	runGit(t, git, first, "remote", "add", "origin", "https://github.com/agenxy/lanes.git")
	runGit(t, git, second, "remote", "add", "origin", "https://github.com/other/project.git")

	if same, known := SameRepo(Identify(first), Identify(second)); same || !known {
		t.Fatalf("SameRepo(repositories with different remotes) = (%v, %v), want (false, true)", same, known)
	}
}

func TestSameRepoDoesNotGuessBetweenLocalRepositories(t *testing.T) {
	git := requireGit(t)
	root := t.TempDir()
	first := filepath.Join(root, "one", "project")
	second := filepath.Join(root, "two", "project")
	initRepo(t, git, first)
	initRepo(t, git, second)

	if same, known := SameRepo(Identify(first), Identify(second)); same || known {
		t.Fatalf("SameRepo(unrelated repositories without remotes) = (%v, %v), want (false, false)", same, known)
	}
}

func TestIdentifyPlainDirectoryIsUnknown(t *testing.T) {
	git := requireGit(t)
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	plain := filepath.Join(root, "plain")
	initRepo(t, git, repo)
	if err := os.Mkdir(plain, 0o755); err != nil {
		t.Fatal(err)
	}

	plainID := Identify(plain)
	if plainID.WorktreeID != "" {
		t.Errorf("plain directory WorktreeID = %q, want empty", plainID.WorktreeID)
	}
	if same, known := SameRepo(plainID, Identify(repo)); same || known {
		t.Fatalf("SameRepo(plain directory, repository) = (%v, %v), want (false, false)", same, known)
	}
}

func TestRepoIDCacheIsBounded(t *testing.T) {
	cache := newRepoIDCache(2)
	cache.add("one", RepoID{WorktreeID: "one"})
	cache.add("two", RepoID{WorktreeID: "two"})
	cache.add("three", RepoID{WorktreeID: "three"})

	if len(cache.entries) != 2 {
		t.Fatalf("cache retained %d entries, want its limit of 2", len(cache.entries))
	}
	if _, ok := cache.get("one"); ok {
		t.Error("cache retained its oldest entry after reaching its bound")
	}
	if _, ok := cache.get("three"); !ok {
		t.Error("cache evicted the entry it had just added")
	}
}

func requireGit(t *testing.T) string {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is unavailable")
	}
	return git
}

func initRepo(t *testing.T, git, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, git, dir, "init")
	runGit(t, git, dir, "config", "user.name", "Lanes Test")
	runGit(t, git, dir, "config", "user.email", "lanes@example.invalid")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, git, dir, "add", "README.md")
	runGit(t, git, dir, "commit", "-m", "fixture")
}

func runGit(t *testing.T, git, dir string, args ...string) {
	t.Helper()
	argv := append([]string{"-C", dir}, args...)
	cmd := exec.Command(git, argv...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}

func realPath(t *testing.T, path string) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return real
}

// The label has to survive the two things agents actually do: work from a
// subdirectory, and work in a directory that is not a checkout at all.
func TestProjectNameNamesTheProjectNotTheDirectory(t *testing.T) {
	git := requireGit(t)
	root := t.TempDir()

	repo := filepath.Join(root, "payments-api")
	initRepo(t, git, repo)
	deep := filepath.Join(repo, "internal", "store")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}

	// An agent that has cd'd into internal/store is still working on
	// payments-api. Naming the row "store" tells the human nothing, and two
	// agents in different repositories both sitting in internal/ would render
	// identically: the exact failure the label exists to remove.
	if got := ProjectName(deep); got != "payments-api" {
		t.Errorf("ProjectName(subdir) = %q, want the project name %q", got, "payments-api")
	}
	if got := ProjectName(repo); got != "payments-api" {
		t.Errorf("ProjectName(root) = %q, want %q", got, "payments-api")
	}

	// Not a checkout: say nothing rather than guessing. The basename of an
	// arbitrary directory is not a project, and a confident wrong label is
	// worse on a board than a blank, because a blank prompts a look at the cwd.
	plain := filepath.Join(root, "just-a-folder")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := ProjectName(plain); got != "" {
		t.Errorf("ProjectName(non-repo) = %q, want \"\"", got)
	}
	if got := ProjectName(""); got != "" {
		t.Errorf("ProjectName(\"\") = %q, want \"\"", got)
	}
}

// A path whose identity was refreshed must be usable from the cache afterwards.
//
// The first revalidation fix left `add` refusing to overwrite an existing key,
// which was correct while entries could never go stale and wrong the moment they
// could: a reused checkout path re-resolved correctly, failed to store the
// answer, and kept the stale entry, so every later lookup invoked Git again and
// the old value was never actually removed. Correctness survived; cost did not.
// A passing suite says nothing about that, which is why it is asserted here.
func TestARefreshedIdentityReplacesTheStaleEntry(t *testing.T) {
	git := requireGit(t)
	root := t.TempDir()
	dir := filepath.Join(root, "reused")

	initRepo(t, git, dir)
	runGit(t, git, dir, "remote", "add", "origin", "https://github.com/acme/one.git")
	if first := Identify(dir); !strings.Contains(first.remote, "one") {
		t.Fatalf("fixture did not resolve: remote is %q", first.remote)
	}

	// Same path, a different repository.
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	initRepo(t, git, dir)
	runGit(t, git, dir, "remote", "add", "origin", "https://github.com/acme/two.git")

	refreshed := Identify(Canonical(dir))
	if !strings.Contains(refreshed.remote, "two") {
		t.Fatalf("the replaced checkout was not re-resolved: remote is %q", refreshed.remote)
	}
	entry, ok := identifiedRepos.entries[Canonical(dir)]
	if !ok {
		t.Fatal("nothing was cached for the path after a successful refresh")
	}
	if !strings.Contains(entry.id.remote, "two") {
		t.Errorf("the cache still holds the previous repository: remote is %q", entry.id.remote)
	}
}

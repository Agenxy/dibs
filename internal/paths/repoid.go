package paths

import (
	"context"
	"net/url"
	"os"
	"os/exec"
	pathpkg "path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	repoIDCacheLimit = 256
	gitIdentityLimit = time.Second
)

// RepoID is the Git identity of one checkout. WorktreeID names the individual
// checkout; SameRepo deliberately uses the unexported common-directory and
// remote identities to decide whether two checkouts are one project.
//
// Raw cwd prefixes used to stand in for repository identity. That made linked
// worktrees look separate and made directory layout, rather than Git, decide
// whether repository-scoped references could interact. WorktreeID is therefore
// the real top-level path Git reports, not the directory passed to Identify.
// It is empty when the directory could not be identified as a Git checkout.
type RepoID struct {
	WorktreeID string

	commonDir  string
	remote     string
	repository bool
}

var identifiedRepos = newRepoIDCache(repoIDCacheLimit)

// Identify returns dir's Git repository identity. It never panics or returns an
// error: absence of Git, a non-repository directory, malformed Git output, and
// a Git command that exceeds one second all produce an unknown RepoID.
//
// Identification invokes Git only on a cache miss. The process-wide cache is
// bounded to avoid turning every cwd ever reported by a long-running fleet into
// permanent daemon memory. Results have no time-based expiry: from the caller's
// point of view an observed identity is stable until it is evicted, and a slow
// or missing Git executable cannot make matching intermittently change answers.
func Identify(dir string) RepoID {
	if dir == "" {
		return RepoID{}
	}
	dir = Canonical(dir)
	if id, ok := identifiedRepos.get(dir); ok {
		return id
	}
	id := identifyRepo(dir)
	identifiedRepos.add(dir, id)
	return id
}

// SameRepo distinguishes evidence of sameness, evidence of separation, and no
// evidence. Only a shared Git common directory or normalized primary remote is
// positive evidence of sameness. Two non-empty, unequal remotes are positive
// evidence of separation.
//
// In particular, different common directories with no remotes are UNKNOWN.
// Separate local clones often have the same basename; guessing from that name,
// or from cwd containment, recreates the raw-prefix failure this API removes.
func SameRepo(a, b RepoID) (same, known bool) {
	if !a.repository || !b.repository {
		return false, false
	}
	if a.commonDir != "" && a.commonDir == b.commonDir {
		return true, true
	}
	if a.remote == "" || b.remote == "" {
		return false, false
	}
	return a.remote == b.remote, true
}

func identifyRepo(dir string) RepoID {
	git, err := exec.LookPath("git")
	if err != nil {
		return RepoID{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitIdentityLimit)
	defer cancel()

	output, err := gitOutput(ctx, git, dir, "rev-parse", "--show-toplevel", "--git-common-dir")
	if err != nil {
		return RepoID{}
	}
	lines := strings.Split(strings.TrimSuffix(output, "\n"), "\n")
	if len(lines) != 2 || lines[0] == "" || lines[1] == "" {
		return RepoID{}
	}
	worktree := Canonical(resolveGitPath(dir, lines[0]))
	commonDir := Canonical(resolveGitPath(dir, lines[1]))
	return RepoID{
		WorktreeID: worktree,
		commonDir:  commonDir,
		remote:     primaryRemote(ctx, git, worktree),
		repository: true,
	}
}

func gitOutput(ctx context.Context, git, dir string, args ...string) (string, error) {
	argv := append([]string{"-C", dir}, args...)
	cmd := exec.CommandContext(ctx, git, argv...) // #nosec G204 -- LookPath resolved Git; exec never invokes a shell
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0")
	output, err := cmd.Output()
	return string(output), err
}

func resolveGitPath(dir, gitPath string) string {
	if filepath.IsAbs(gitPath) {
		return gitPath
	}
	return filepath.Join(dir, gitPath)
}

func primaryRemote(ctx context.Context, git, worktree string) string {
	output, err := gitOutput(ctx, git, worktree, "config", "--local", "--get-regexp", `^remote\..*\.url$`)
	if err != nil {
		return ""
	}
	type candidate struct {
		name string
		url  string
	}
	var remotes []candidate
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		separator := strings.IndexAny(line, " \t")
		if separator < 0 {
			continue
		}
		key := line[:separator]
		remoteURL := strings.TrimSpace(line[separator:])
		name := strings.TrimSuffix(strings.TrimPrefix(key, "remote."), ".url")
		if name == key || name == "" || remoteURL == "" {
			continue
		}
		remotes = append(remotes, candidate{name: name, url: remoteURL})
	}
	if len(remotes) == 0 {
		return ""
	}
	// origin is Git's conventional primary remote. A repository without one is
	// still identifiable, so choose the first remote name deterministically.
	sort.SliceStable(remotes, func(i, j int) bool {
		if remotes[i].name == "origin" {
			return remotes[j].name != "origin"
		}
		if remotes[j].name == "origin" {
			return false
		}
		return remotes[i].name < remotes[j].name
	})
	return normalizeRemote(remotes[0].url, worktree)
}

func normalizeRemote(remote, worktree string) string {
	remote = strings.TrimSpace(remote)
	if remote == "" {
		return ""
	}
	if filepath.VolumeName(remote) != "" {
		return normalizeLocalRemote(remote, worktree)
	}
	if parsed, err := url.Parse(remote); err == nil && parsed.Scheme != "" {
		return normalizeURLRemote(parsed, worktree)
	}
	if host, remotePath, ok := splitSCPRemote(remote); ok {
		return joinRemoteIdentity(host, remotePath)
	}
	return normalizeLocalRemote(remote, worktree)
}

func normalizeURLRemote(parsed *url.URL, worktree string) string {
	switch strings.ToLower(parsed.Scheme) {
	case "file":
		if parsed.Path == "" {
			return ""
		}
		if parsed.Host != "" && !strings.EqualFold(parsed.Host, "localhost") {
			return joinRemoteIdentity(parsed.Host, parsed.Path)
		}
		return normalizeLocalRemote(parsed.Path, worktree)
	case "git", "http", "https", "ssh":
		host := parsed.Hostname()
		if port := normalizedPort(parsed.Scheme, parsed.Port()); port != "" {
			host += ":" + port
		}
		return joinRemoteIdentity(host, parsed.Path)
	default:
		return ""
	}
}

func joinRemoteIdentity(host, remotePath string) string {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	remotePath = normalizeRemotePath(remotePath)
	if host == "" || remotePath == "" || remotePath == "." {
		return ""
	}
	return host + "/" + remotePath
}

func splitSCPRemote(remote string) (host, remotePath string, ok bool) {
	colon := strings.IndexByte(remote, ':')
	if colon <= 1 || strings.ContainsAny(remote[:colon], `/\\`) {
		return "", "", false
	}
	host = remote[:colon]
	if at := strings.LastIndexByte(host, '@'); at >= 0 {
		host = host[at+1:]
	}
	if host == "" || remote[colon+1:] == "" {
		return "", "", false
	}
	return host, remote[colon+1:], true
}

func normalizeRemotePath(remotePath string) string {
	clean := strings.TrimPrefix(pathpkg.Clean("/"+remotePath), "/")
	clean = strings.TrimSuffix(clean, ".git")
	return strings.TrimSuffix(clean, "/")
}

func normalizeLocalRemote(remote, worktree string) string {
	if strings.HasPrefix(remote, "~") {
		return "" // expansion depends on the Git server or invoking user's home
	}
	if !filepath.IsAbs(remote) {
		remote = filepath.Join(worktree, remote)
	}
	return "file:" + Canonical(remote)
}

func normalizedPort(scheme, port string) string {
	switch {
	case port == "":
		return ""
	case strings.EqualFold(scheme, "ssh") && port == "22":
		return ""
	case strings.EqualFold(scheme, "http") && port == "80":
		return ""
	case strings.EqualFold(scheme, "https") && port == "443":
		return ""
	default:
		return port
	}
}

type repoIDCache struct {
	mu             sync.Mutex
	limit          int
	entries        map[string]RepoID
	insertionOrder []string
}

func newRepoIDCache(limit int) *repoIDCache {
	return &repoIDCache{limit: limit, entries: make(map[string]RepoID, limit)}
}

func (c *repoIDCache) get(dir string) (RepoID, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	id, ok := c.entries[dir]
	return id, ok
}

func (c *repoIDCache) add(dir string, id RepoID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.entries[dir]; exists {
		return
	}
	// Created-order eviction, not LRU: hits stay read-only from the caller's
	// perspective, while the fixed ceiling is the property that matters here.
	if len(c.insertionOrder) == c.limit {
		delete(c.entries, c.insertionOrder[0])
		c.insertionOrder = c.insertionOrder[1:]
	}
	c.entries[dir] = id
	c.insertionOrder = append(c.insertionOrder, dir)
}

// ProjectName is the human label for the project a directory belongs to: the
// basename of the Git worktree, or "" when the directory is not a checkout.
//
// It answers the question a board cannot answer without it. A fleet spread over
// three repositories showed three agents on branch "main" and one column of
// identical-looking rows, because "main" is not a distinguishing fact and the
// full cwd is too long to scan. The project name is the shortest thing that
// separates them.
//
// It is a LABEL, not an identity. Two unrelated clones can both be called "api",
// so nothing may group, match or authorise by this string; SameRepo is the only
// thing entitled to decide that two directories are one project. The full cwd
// travels beside it for anyone who has to disambiguate.
//
// The worktree basename rather than the basename of dir itself, because an agent
// that has cd'd into a subdirectory is still working on the same project, and
// "internal" or "src" names nothing.
func ProjectName(dir string) string {
	id := Identify(dir)
	if id.WorktreeID == "" {
		return ""
	}
	return filepath.Base(id.WorktreeID)
}

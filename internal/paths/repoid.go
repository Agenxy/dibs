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
	"syscall"
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
	roots      string // space-joined, sorted root commit ids
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

// Identity returns the two facts that decide whether two checkouts are one
// project: the Git common directory and the normalized primary remote. Either
// may be empty, and ok is false when the directory is not a checkout at all.
//
// Exposed for RECORDING. Deciding this at read time needs Git, which the pure
// core cannot call and the single-writer loop cannot afford; recording both
// values on the op at registration lets the fold compare them deterministically,
// and lets a replay reach the same verdict years later on a machine where the
// checkout no longer exists.
//
// Both values, not a single key, because the answer is three-valued. Two unequal
// remotes are evidence of SEPARATION, whereas one absent remote is merely an
// absence of evidence, and collapsing those into one string loses the
// distinction that keeps a missing fact from reading as a difference.
func (r RepoID) Identity() (commonDir, remote, roots string, ok bool) {
	return r.commonDir, r.remote, r.roots, r.repository
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
		roots:      rootCommits(ctx, git, worktree),
		repository: true,
	}
}

// rootCommits returns the parentless commits of a repository, sorted and space
// joined, or "" when there are none to report.
//
// This is the only fact that separates two cases nothing else can tell apart: a
// clone whose origin has been removed, and a repository created locally with
// `git init`. Both present as "a common directory of their own, with no remote",
// and one is the same project while the other is a stranger. Two clones share a
// root commit however the remote has been edited since; two independent
// histories do not, because a root commit hashes its own tree, author and time.
//
// Empty for a repository with no commits yet, which is honest: an unborn HEAD
// has no history to share. Note also that a shallow clone reports its shallow
// boundary rather than the true root, so two shallow clones cut at different
// depths disagree. They are clones, so they carry a remote, and the remote is
// consulted first.
func rootCommits(ctx context.Context, git, worktree string) string {
	// A shallow clone does not HAVE its root commit: what rev-list reports is the
	// boundary where the clone was cut, so two shallow clones of one repository at
	// different depths report different ids and look like strangers. Say nothing
	// rather than something false. Absence of evidence has to stay absence of
	// evidence, which is the mistake this whole area keeps making.
	if shallow, err := gitOutput(ctx, git, worktree, "rev-parse", "--is-shallow-repository"); err != nil ||
		strings.TrimSpace(shallow) == "true" {
		return ""
	}
	// --no-replace-objects because `git replace --graft` rewrites what rev-list
	// calls a root, and it is a LOCAL decoration: two clones of one upstream
	// disagree about their roots the moment one of them uses it. Identity must
	// come from the objects themselves, not from a local view of them.
	out, err := gitOutput(ctx, git, worktree, "--no-replace-objects", "rev-list", "--max-parents=0", "HEAD")
	if err != nil {
		return ""
	}
	ids := strings.Fields(out)
	if len(ids) == 0 {
		return ""
	}
	sort.Strings(ids)
	return strings.Join(ids, " ")
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
	// The RAW configured url is not the one Git would use. `url.<base>.insteadOf`
	// rewrites it, so two clones of one upstream can carry different strings in
	// config and fetch from the same place, and comparing the strings called them
	// different projects. Ask Git for the effective url instead; --get-url expands
	// and prints without contacting the remote, so this stays local and cheap.
	effective, err := gitOutput(ctx, git, worktree, "ls-remote", "--get-url", remotes[0].name)
	if url := strings.TrimSpace(effective); err == nil && url != "" {
		return normalizeRemote(url, worktree)
	}
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
	// The PATH is lowercased too, not just the host. Every forge people
	// actually use treats it case-insensitively: `git ls-remote` returns the
	// same HEAD for Agenxy/Lanes and agenxy/lanes, so two clones of one
	// repository can spell their origin differently and are not two projects.
	//
	// The cost is a self-hosted server with case-sensitive paths serving both
	// `team/Api` and `team/api` as different repositories, where this would call
	// them one. That is a warning somebody dismisses. The other direction lost a
	// real collision, which is the expensive half.
	return host + "/" + strings.ToLower(remotePath)
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
	entries        map[string]repoIDEntry
	insertionOrder []string
}

// repoIDEntry is a resolved identity plus the file whose identity says whether
// it is still true.
type repoIDEntry struct {
	id      RepoID
	watch   string
	watchID fileIdentity
}

func newRepoIDCache(limit int) *repoIDCache {
	return &repoIDCache{limit: limit, entries: make(map[string]repoIDEntry, limit)}
}

func (c *repoIDCache) get(dir string) (RepoID, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[dir]
	if !ok || identifyFile(e.watch) != e.watchID {
		return RepoID{}, false
	}
	return e.id, true
}

// watchPath is the file whose identity decides whether a cached answer still
// describes what is on disk.
//
// For a repository it is the Git common directory, which a fresh clone
// recreates. For a directory that is NOT one it is the `.git` that would appear
// if somebody ran `git init` there, and its absence is recorded just as
// precisely as its presence: an unknown answer that never recovers is the same
// bug as a stale known one, and a review found both. The second is the one I
// predicted and skipped, which is its own lesson.
func watchPath(dir string, id RepoID) string {
	if id.repository && id.commonDir != "" {
		return id.commonDir
	}
	return filepath.Join(dir, ".git")
}

// fileIdentity is what a stat says about which file this is, as opposed to what
// it is named. Zero when the path could not be stat-ed at all.
type fileIdentity struct {
	// Widths follow the platform rather than the field: st_dev is int32 on
	// darwin and uint64 on linux, and a conversion that narrows on either would
	// make two different files compare equal. Keeping them as the signed and
	// unsigned types Go already gives us avoids the question entirely.
	device int64
	inode  uint64
}

func identifyFile(path string) fileIdentity {
	info, err := os.Stat(path)
	if err != nil {
		return fileIdentity{}
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fileIdentity{}
	}
	return fileIdentity{device: int64(st.Dev), inode: st.Ino}
}

// sameFile reports whether path is still the same file it was when observed.
// An unobservable identity (no stat, or a platform that does not expose one)
// counts as unchanged: this exists to catch a replaced checkout, not to make
// identification fail where it cannot be verified.
func sameFile(path string, observed fileIdentity) bool {
	if observed == (fileIdentity{}) || path == "" {
		return true
	}
	return identifyFile(path) == observed
}

func (c *repoIDCache) add(dir string, id RepoID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	watch := watchPath(dir, id)
	entry := repoIDEntry{id: id, watch: watch, watchID: identifyFile(watch)}
	// REPLACES an existing entry rather than refusing. Refusing was correct
	// while entries could never go stale, and stopped being correct the moment
	// they could: a revalidated miss re-resolved, could not store the answer,
	// and left the old entry in place, so that path invoked Git on every
	// registration forever and the stale value was never actually removed.
	if _, exists := c.entries[dir]; exists {
		c.entries[dir] = entry
		return
	}
	// Created-order eviction, not LRU: hits stay read-only from the caller's
	// perspective, while the fixed ceiling is the property that matters here.
	if len(c.insertionOrder) == c.limit {
		delete(c.entries, c.insertionOrder[0])
		c.insertionOrder = c.insertionOrder[1:]
	}
	c.entries[dir] = entry
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

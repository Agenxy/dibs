// Package selfupdate answers two questions about this install: is there a
// newer release, and can this machine fetch it.
//
// `dibs upgrade` moves a running fleet onto the dibd you have INSTALLED, and
// its help has always said, in as many words, that it does not fetch
// anything. That is a defensible split and it left a hole with nothing in it:
// nothing anywhere told an operator a newer version existed. A coordination
// service that never mentions its own updates is one people run for months
// behind, which is the same failure REQUIREMENTS R12 is about from the other
// end: an upgrade nobody dares perform, and an upgrade nobody knows to.
//
// What this package will NOT do is as much of the design as what it will:
//
//   - It does not touch a Homebrew install. Replacing a file brew owns makes
//     brew and the disk disagree, and the next `brew upgrade` puts the old
//     build back with no explanation. The operator is told the command that
//     works instead.
//   - It does not run on a schedule and the daemon never calls it. The check
//     is something a person or `dibs doctor` asks for, so the only requests
//     Dibs makes to the network are ones somebody asked for.
//   - It does not install what it cannot verify. The release publishes a
//     checksum file and a cosign bundle over it, and both are checked; a
//     machine with no cosign is told so and refused rather than reassured.
package selfupdate

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/agenxy/dibs/internal/release"
)

// Repo is where releases are published. Not configurable: an update source is
// a trust decision, and one that could be pointed elsewhere by an environment
// variable is a way to install somebody else's binary as this machine's
// daemon.
const Repo = "Agenxy/dibs"

// latestURL is GitHub's own answer to "what is the current release". It
// excludes prereleases and drafts, which is what makes it the right question:
// a prerelease is not something an operator should be moved onto by a command
// that says "upgrade".
const latestURL = "https://api.github.com/repos/" + Repo + "/releases/latest"

// Release is the published release this machine could move to.
type Release struct {
	Version string // without the leading v
	Tag     string // as published, with the v
	URL     string // the human page, for a message that names where to look
}

// Latest asks GitHub what the current release is.
//
// Unauthenticated, because a public release list is public and asking for a
// token to read it would be a reason not to check. That caps the caller at
// GitHub's anonymous rate, which is generous for a command a person runs, and
// the refusal is reported as itself rather than as "no update": an operator
// who is told they are current when nothing was checked is worse off than one
// told the check did not happen.
func Latest(ctx context.Context, c *http.Client) (Release, error) {
	return latestFrom(ctx, c, latestURL)
}

// latestFrom is Latest against a stated endpoint, so the parsing and the
// refusals can be tested against a server rather than against GitHub. The
// endpoint is not a caller's to choose: Latest is the only way in.
func latestFrom(ctx context.Context, c *http.Client, from string) (Release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, from, nil)
	if err != nil {
		return Release{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := c.Do(req)
	if err != nil {
		return Release{}, fmt.Errorf("asking %s for the current release: %w", Repo, err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusForbidden, http.StatusTooManyRequests:
		return Release{}, fmt.Errorf("github is rate limiting this machine, so the current "+
			"version of %s is unknown right now (it resets within the hour)", Repo)
	case http.StatusNotFound:
		return Release{}, fmt.Errorf("%s has published no release yet", Repo)
	default:
		return Release{}, fmt.Errorf("asking %s for the current release: HTTP %d",
			Repo, resp.StatusCode)
	}
	var body struct {
		Tag   string `json:"tag_name"`
		URL   string `json:"html_url"`
		Draft bool   `json:"draft"`
		Pre   bool   `json:"prerelease"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return Release{}, fmt.Errorf("reading the release %s published: %w", Repo, err)
	}
	if body.Tag == "" {
		return Release{}, fmt.Errorf("%s answered with a release that has no tag", Repo)
	}
	return Release{
		Version: strings.TrimPrefix(body.Tag, "v"),
		Tag:     body.Tag,
		URL:     body.URL,
	}, nil
}

// Standing is what a comparison between this build and the published one
// means for the operator.
type Standing int

const (
	// Unknowable means this binary was not built from a release, so there is
	// nothing to compare. `internal/build` reports a bare revision or the word
	// "devel" for that case rather than a number, deliberately, and inventing
	// an ordering here would tell a developer their own working build is out
	// of date every time they ran the check.
	Unknowable Standing = iota
	// Current means nothing newer is published.
	Current
	// Behind means a newer release exists.
	Behind
	// Ahead means this build is newer than anything published, which is what a
	// release branch looks like between `task release` and the tag.
	Ahead
)

// Compare places this build against a published release.
func Compare(have, published string) Standing {
	if !release.Comparable(have) || !release.Comparable(published) {
		return Unknowable
	}
	c, err := release.Compare(published, have)
	switch {
	case err != nil:
		return Unknowable
	case c > 0:
		return Behind
	case c < 0:
		return Ahead
	default:
		return Current
	}
}

// ArchiveName is the release asset for one platform, as GoReleaser names it.
//
// Built from the same parts the archive is, rather than discovered by reading
// the release's asset list, so a missing platform is reported as the platform
// it is instead of as "not found among 8 files". Dibs publishes no macOS Intel
// build, which is the one absence an operator is likely to meet.
func ArchiveName(version, goos, goarch string) (string, error) {
	if goos == "darwin" && goarch == "amd64" {
		return "", fmt.Errorf("dibs publishes no macOS Intel build: it is arm64 only on " +
			"macOS, and Intel Macs build from source (`task install` in a checkout)")
	}
	switch goos {
	case "darwin", "linux":
	default:
		return "", fmt.Errorf("dibs publishes no %s build", goos)
	}
	return fmt.Sprintf("dibs_%s_%s_%s.tar.gz", version, goos, goarch), nil
}

// ChecksumsName and BundleName are the two files that make an archive
// checkable: the digests of every asset, and the cosign bundle over that file.
const (
	ChecksumsName = "checksums.txt"
	BundleName    = "checksums.txt.bundle"
)

// releaseBase is where release assets live. A constant for the same reason
// Repo is: an update source is a trust decision.
const releaseBase = "https://github.com/" + Repo + "/releases/download"

// DownloadURL is where one asset of one release lives.
func DownloadURL(tag, asset string) string { return assetURL(releaseBase, tag, asset) }

func assetURL(base, tag, asset string) string { return base + "/" + tag + "/" + asset }

// ChecksumFor reads one asset's digest out of a checksums file.
//
// The format is `<hex>  <name>`, two spaces, one asset per line. An asset that
// is not named in the file is an error and never an empty digest: a caller
// that compared against "" would accept anything, which is the shape of
// verification bug that verifies nothing and says it did.
func ChecksumFor(checksums, asset string) (string, error) {
	s := bufio.NewScanner(strings.NewReader(checksums))
	for s.Scan() {
		hex, name, found := strings.Cut(strings.TrimSpace(s.Text()), "  ")
		if !found {
			continue
		}
		if strings.TrimSpace(name) == asset {
			hex = strings.TrimSpace(hex)
			if len(hex) != 64 {
				return "", fmt.Errorf("the digest listed for %s is %d characters, not a "+
					"sha256", asset, len(hex))
			}
			return strings.ToLower(hex), nil
		}
	}
	if err := s.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("%s names no digest for %s, so there is nothing to check the "+
		"download against", ChecksumsName, asset)
}

// Managed reports the package manager that owns this install, or "" when
// nothing does.
//
// Self-replacing a file brew owns leaves brew's record and the disk
// disagreeing: `brew list --versions` reports the old one, the next
// `brew upgrade` puts the old build back, and the operator has no way to tell
// why their upgrade came undone. Detected by PATH rather than by asking brew,
// because asking costs a process on every check and the answer is a path
// either way.
func Managed(binary string) string {
	if resolved, err := filepath.EvalSymlinks(binary); err == nil {
		binary = resolved
	}
	brewed := []string{
		"/opt/homebrew/", "/usr/local/Homebrew/", "/usr/local/Cellar/",
		"/usr/local/Caskroom/", "/home/linuxbrew/.linuxbrew/",
	}
	for _, prefix := range brewed {
		if strings.HasPrefix(binary, prefix) {
			return "homebrew"
		}
	}
	return ""
}

// ManagedFix is what to run instead, for an install this must not touch.
func ManagedFix(by string) string {
	if by == "homebrew" {
		return "brew upgrade --cask dibs"
	}
	return ""
}

// CheckTimeout bounds the one network call. Short, because this runs inside
// `dibs doctor`, which an operator reaches for when something is already
// wrong: a diagnosis that hangs on a network the machine cannot reach is a
// diagnosis nobody gets.
const CheckTimeout = 4 * time.Second

// SkipEnv turns the check off for a machine that should make no outbound
// request at all. The daemon never checks on its own; this is for the
// operator who wants `dibs doctor` not to either.
const SkipEnv = "DIBS_NO_UPDATE_CHECK"

// Skipped reports whether the operator has turned the check off.
func Skipped() bool {
	v := strings.TrimSpace(os.Getenv(SkipEnv))
	return v != "" && v != "0" && !strings.EqualFold(v, "false")
}

// Platform is this machine, as the release names it.
func Platform() (goos, goarch string) { return runtime.GOOS, runtime.GOARCH }

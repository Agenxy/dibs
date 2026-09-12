package overlap

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Payload is an index as an agent ships it: everything the daemon would have
// read out of the checkout itself, read instead by the side that has access.
//
// Matching mines the repository: `git ls-files` for paths, `git log` for
// commit subjects and the files each touched. So the daemon needed read
// access to every tree its agents work in, and on macOS that is a genuinely
// bad ask: a daemon started by launchd is not granted ~/Desktop, ~/Documents
// or ~/Downloads, and /usr/bin/git blocks on a permission prompt no
// background process can show. The agent already has the access, is already
// inside the repository, and is already talking to the daemon; both things
// the index needs are bounded, and neither is file contents. Issue #19.
//
// Agent-supplied data authorises nothing. The daemon accepts a payload only
// for the tree the registered agent's own cwd is inside, scores only agents
// in that tree with it, and keeps its own reading of which project a tree is
// wherever it can read the tree at all.
type Payload struct {
	// Root is the checkout root on the agent's machine, which is also where
	// the agent's cwd is, so the daemon can check that the two agree.
	Root string `json:"root"`
	// Fingerprint is CoChange.Fingerprint as the shipper mined it.
	Fingerprint string `json:"fingerprint,omitempty"`
	// The project identity as paths.Identify reads it beside the checkout,
	// so an index shipped for one clone can be told from one for another
	// clone of the same project. Recorded as the agent's word, and used only
	// to pair indexes of one project; never to decide a claim or a role.
	RepoDir    string `json:"repo_dir,omitempty"`
	RepoRemote string `json:"repo_remote,omitempty"`
	RepoRoots  string `json:"repo_roots,omitempty"`
	// Files is `git ls-files`; Commits is what MineCoChange keeps of `git log`.
	Files   []string `json:"files"`
	Commits []Commit `json:"commits"`
}

// Bounds on a shipped index. The daemon's memory is what a payload lands in,
// and it arrives from an agent: measured, this repository is ~30 KB for 144
// commits and about 210 bytes a commit, so the byte cap is thirty times the
// largest index the default window can produce.
const (
	MaxPayloadBytes   = 16 << 20
	MaxPayloadFiles   = 200_000
	MaxPayloadCommits = 2000 // DefaultCoChangeOptions.MaxCommits, which is not a constant
)

// Validate refuses a payload that could not have come from a checkout, or
// that exceeds what the daemon is willing to hold for one.
func (p *Payload) Validate() error {
	switch {
	case p == nil:
		return errors.New("no payload")
	case strings.TrimSpace(p.Root) == "" || !strings.HasPrefix(p.Root, "/"):
		return errors.New("root must be an absolute path")
	case len(p.Files) == 0:
		return errors.New("no tracked files: nothing to index")
	case len(p.Files) > MaxPayloadFiles:
		return fmt.Errorf("%d files exceeds the %d the daemon will index", len(p.Files), MaxPayloadFiles)
	case len(p.Commits) > MaxPayloadCommits:
		return fmt.Errorf("%d commits exceeds the %d window", len(p.Commits), MaxPayloadCommits)
	}
	for _, f := range p.Files {
		if f == "" || strings.HasPrefix(f, "/") || strings.Contains(f, "..") {
			return fmt.Errorf("file %q is not a repository-relative path", f)
		}
	}
	return nil
}

// Ship reads what Payload carries out of the checkout at root, on the side
// that can read it.
func Ship(ctx context.Context, root string, opt CoChangeOptions) (*Payload, error) {
	cc, err := MineCoChange(ctx, root, opt)
	if err != nil {
		return nil, err
	}
	files, err := TrackedFiles(ctx, root)
	if err != nil {
		return nil, err
	}
	return &Payload{Root: root, Fingerprint: cc.Fingerprint(), Files: files, Commits: cc.Records()}, nil
}

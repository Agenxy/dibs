package overlap

import (
	"bufio"
	"context"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Commit is one commit's description and what it touched: the project
// describing its own work, in the words the project uses.
type Commit struct {
	Subject string
	Files   []string
}

// CoChange is "which files change together in this repository", mined from git.
//
// This is the signal that makes tier 0 worth having rather than a placeholder.
// Token matching can only relate work that shares words; co-change relates work
// that shares HISTORY, which is where a project's real coupling lives. If every
// change to the ledger has also touched the engine for two years, then an agent
// declaring work on the ledger is going to touch the engine, and no amount of
// reading the two declarations in English would tell you that.
//
// It is also the cheapest project-specific evidence available anywhere: it
// needs no model, no download, no network, and it is already on disk. A
// coordination service whose overlap detection only works after a 2 GB download
// is a service that is off on the day it was needed.
type CoChange struct {
	// Messages is the commit log as (description, files) pairs, for scorers that
	// want to match a declaration against how this project talks about its work
	// rather than against its file names.
	Messages []Commit

	mu sync.RWMutex
	// commits[f] is how many sampled commits touched f.
	commits map[string]int
	// pairs[a][b] is how many sampled commits touched both.
	pairs map[string]map[string]int
	n     int // commits sampled
}

// CoChangeOptions bounds the mining. Both bounds exist for measured reasons.
type CoChangeOptions struct {
	// MaxCommits caps how far back to look. History is not uniformly useful:
	// a five-year-old refactor describes a layout that no longer exists.
	MaxCommits int
	// MaxFilesPerCommit discards sweeping commits. A commit touching 200 files
	// (a vendor drop, a licence header sweep, a mass rename) contributes
	// 19,900 pairs, every one of them a coincidence. Left in, these dominate
	// the index and make everything look coupled to everything.
	MaxFilesPerCommit int
}

// DefaultCoChangeOptions are tuned for a working repository, not a benchmark.
var DefaultCoChangeOptions = CoChangeOptions{MaxCommits: 2000, MaxFilesPerCommit: 25}

// Separators for git's --pretty=format output. ASCII RS and US: legal inside an
// execve argument, and absent from any realistic commit message. See the note
// in MineCoChange for why the obvious choice, NUL, cannot be used.
const (
	recSep = "\x1e"
	fldSep = "\x1f"
)

// MineCoChange builds the index by reading git log. Read-only; it shells out to
// git rather than linking a git library because the repository is the user's
// and `git log` is the one interface guaranteed not to corrupt it.
func MineCoChange(ctx context.Context, repo string, opt CoChangeOptions) (*CoChange, error) {
	if opt.MaxCommits <= 0 {
		opt.MaxCommits = DefaultCoChangeOptions.MaxCommits
	}
	if opt.MaxFilesPerCommit <= 0 {
		opt.MaxFilesPerCommit = DefaultCoChangeOptions.MaxFilesPerCommit
	}
	cc := &CoChange{commits: map[string]int{}, pairs: map[string]map[string]int{}}

	// --no-merges: a merge's file list is the union of both sides and pairs up
	// files that were never edited together by anybody.
	//
	// recSep is ASCII RS, not NUL. NUL is the obvious record separator and is
	// impossible here: execve arguments are NUL-terminated C strings, so Go
	// refuses an argv entry containing one and the call fails with a bare
	// "fork/exec: invalid argument". RS is legal in argv and does not occur in
	// commit messages.
	// #nosec G204 -- no shell is involved: exec.Command passes argv directly,
	// so a path cannot inject arguments. The repository path comes from an
	// operator flag or config, never from an agent.
	// %s is the commit SUBJECT, and it comes free: the same log call already
	// reads the file list. A commit message is a description of work in the
	// project's own words, and its files are what that work touched, which is
	// precisely the pairing `lanes calibrate` already treats as ground truth.
	// Reading it here is what lets tier 0 answer a declaration that names no
	// file, without a model.
	cmd := exec.CommandContext(ctx, "git", "-C", repo, "log",
		"--no-merges", "--name-only", "--pretty=format:"+recSep+"%s"+fldSep,
		"-n", strconv.Itoa(opt.MaxCommits))
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	for _, block := range strings.Split(string(out), recSep) {
		subject, rest, found := strings.Cut(block, fldSep)
		if !found {
			rest = block
		}
		var files []string
		sc := bufio.NewScanner(strings.NewReader(rest))
		for sc.Scan() {
			if f := strings.TrimSpace(sc.Text()); f != "" {
				files = append(files, f)
			}
		}
		if len(files) < 1 || len(files) > opt.MaxFilesPerCommit {
			continue
		}
		cc.add(files)
		if s := strings.TrimSpace(subject); s != "" {
			cc.Messages = append(cc.Messages, Commit{Subject: s, Files: files})
		}
	}
	return cc, nil
}

func (c *CoChange) add(files []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++
	sort.Strings(files)
	for i, a := range files {
		c.commits[a]++
		for _, b := range files[i+1:] {
			if a == b {
				continue
			}
			if c.pairs[a] == nil {
				c.pairs[a] = map[string]int{}
			}
			if c.pairs[b] == nil {
				c.pairs[b] = map[string]int{}
			}
			c.pairs[a][b]++
			c.pairs[b][a]++
		}
	}
}

// Commits reports how many sampled commits were mined.
func (c *CoChange) Commits() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.n
}

// Related returns files that historically change with path, scored by
// confidence: P(b changes | a changes).
//
// Conditional probability rather than raw pair count, because raw counts just
// rank the repository's busiest files. README.md co-occurring with everything
// is a fact about README.md, not a relationship: dividing by how often the
// query file itself changes removes exactly that.
//
// minSupport discards pairs seen too rarely to mean anything: two files that
// changed together once, out of one appearance each, have confidence 1.0 and
// tell you nothing at all.
func (c *CoChange) Related(path string, minSupport int, limit int) []File {
	c.mu.RLock()
	defer c.mu.RUnlock()
	base := c.commits[path]
	if base == 0 {
		return nil
	}
	if minSupport < 2 {
		minSupport = 2
	}
	var out []File
	for other, n := range c.pairs[path] {
		if n < minSupport {
			continue
		}
		out = append(out, File{Path: other, Weight: float64(n) / float64(base)})
	}
	return topN(out, limit)
}

// Expand grows a predicted file set by one co-change hop.
//
// One hop, not transitive closure: two hops through a repository's history
// reaches most of it, and a prediction that names everything distinguishes
// nothing. Expanded files are damped by `decay` so a historically-implied file
// never outweighs one the declaration actually pointed at: an inference is
// weaker evidence than a direct hit and the weights have to say so.
func (c *CoChange) Expand(files []File, decay float64, limit int) []File {
	if decay <= 0 {
		decay = 0.5
	}
	merged := make(map[string]float64, len(files)*4)
	for _, f := range files {
		if f.Weight > merged[f.Path] {
			merged[f.Path] = f.Weight
		}
	}
	for _, f := range files {
		for _, rel := range c.Related(f.Path, 2, 10) {
			w := f.Weight * rel.Weight * decay
			if w > merged[rel.Path] {
				merged[rel.Path] = w
			}
		}
	}
	out := make([]File, 0, len(merged))
	for p, w := range merged {
		out = append(out, File{Path: p, Weight: w})
	}
	return topN(out, limit)
}

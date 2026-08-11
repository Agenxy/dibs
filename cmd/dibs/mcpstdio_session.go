package main

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// sessionContext discovers where this agent is working, from what the harness
// already writes to disk. The agent never has to know any of it.
//
// Claude Code keeps a per-process sidecar at ~/.claude/sessions/<pid>.json
// (sessionId, cwd, version, entrypoint): cheap and structured. The session
// TITLE, which is the field a human scanning a fleet actually wants, lives in
// the transcript instead, so it is read separately and with a hard bound: the
// transcripts run to tens of megabytes and we are doing this on registration.
func sessionContext(isClaude bool) map[string]string {
	out := map[string]string{}
	if h, err := os.Hostname(); err == nil {
		out["host"] = strings.TrimSuffix(h, ".local")
	}

	// The bridge is spawned by the harness as a child, so it inherits the
	// agent's working directory. That makes os.Getwd() the universal answer to
	// "which checkout is this agent in": free, and available for every harness.
	//
	// This used to be read only from Claude Code's sidecar, which meant a real
	// opencode run registered an agent with no cwd and no branch at all. Asking
	// the MODEL for them is not a fallback: a live gpt-oss-120b run sent
	// `"cwd":"", "branch":"", "model":""` for every field it was offered.
	// Identity has to be observed, never self-reported.
	if wd, err := os.Getwd(); err == nil && wd != "" {
		out["cwd"] = wd
		if br := gitBranch(wd); br != "" {
			out["branch"] = br
		}
	}

	// Hostname and cwd are universal; everything below is Claude Code's own
	// bookkeeping, and is authoritative where it exists: the sidecar knows the
	// session's real cwd even if the bridge were spawned somewhere else.
	pid := os.Getenv("CLAUDE_PID")
	home, _ := os.UserHomeDir()
	if !isClaude || pid == "" || home == "" {
		return out
	}
	// A pid is digits. Checking that is not ceremony: the value becomes a path
	// component below, and "../../.." is a legal environment variable.
	if _, err := strconv.Atoi(pid); err != nil {
		return out
	}
	var side struct {
		SessionID  string `json:"sessionId"`
		CWD        string `json:"cwd"`
		Entrypoint string `json:"entrypoint"`
	}
	// Reads the user's OWN ~/.claude sidecar. `pid` is validated numeric above,
	// so it cannot carry traversal segments, and this process already runs as
	// the user whose files these are.
	sidecar := filepath.Join(home, ".claude", "sessions", pid+".json")
	b, err := os.ReadFile(sidecar) //nolint:gosec // same-user path; pid validated numeric above
	if err != nil || json.Unmarshal(b, &side) != nil {
		return out
	}
	if side.CWD != "" {
		out["cwd"] = side.CWD
		if br := gitBranch(side.CWD); br != "" {
			out["branch"] = br
		}
	}
	if side.Entrypoint != "" {
		out["surface"] = side.Entrypoint
	}
	if side.SessionID != "" {
		out["session_id"] = side.SessionID
		if t := sessionTitle(home, side.CWD, side.SessionID); t != "" {
			out["title"] = t
		}
	}
	return out
}

// gitBranch reads the checked-out branch. symbolic-ref is used rather than
// `rev-parse --abbrev-ref HEAD` because the latter fails on an unborn branch,
// a fresh repo with no commits yet, which is exactly when a fleet is most
// likely to be spun up on a new project.
func gitBranch(dir string) string {
	// #nosec G204 -- no shell: exec.Command passes argv directly, so a path
	// cannot inject arguments. The value is an operator-supplied directory,
	// never agent input.
	if b, err := exec.Command("git", "-C", dir, "symbolic-ref", "--short", "-q", "HEAD").Output(); err == nil {
		if br := strings.TrimSpace(string(b)); br != "" {
			return br
		}
	}
	// Detached HEAD: report the short sha, which is still what a reader needs.
	// #nosec G204 -- no shell: exec.Command passes argv directly, so a path
	// cannot inject arguments. The value is an operator-supplied directory,
	// never agent input.
	if b, err := exec.Command("git", "-C", dir, "rev-parse", "--short", "HEAD").Output(); err == nil {
		if sha := strings.TrimSpace(string(b)); sha != "" {
			return "detached@" + sha
		}
	}
	return ""
}

// sessionTitle scans the transcript for the newest title. Only the two title
// fields are ever read out; conversation content is never touched. Bounded to
// the tail so a huge transcript costs the same as a small one.
func sessionTitle(home, cwd, sessionID string) string {
	slug := strings.NewReplacer("/", "-", ".", "-").Replace(cwd)
	// #nosec G304 -- the path is the daemon's own data directory, chosen by the
	// operator via -dir/DIBS_DIR. Refusing to open it would mean refusing to
	// run at all.
	f, err := os.Open(filepath.Join(home, ".claude", "projects", slug, sessionID+".jsonl"))
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()

	const tail = 2 << 20 // last 2 MiB is plenty to hold a recent rename
	if st, err := f.Stat(); err == nil && st.Size() > tail {
		if _, err := f.Seek(-tail, 2); err != nil {
			return ""
		}
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<23)
	var custom, ai string
	for sc.Scan() {
		var rec struct {
			CustomTitle string `json:"customTitle"`
			AITitle     string `json:"aiTitle"`
		}
		if json.Unmarshal(sc.Bytes(), &rec) != nil {
			continue // partial first line after seek, or a record we don't care about
		}
		if rec.CustomTitle != "" {
			custom = rec.CustomTitle
		}
		if rec.AITitle != "" {
			ai = rec.AITitle
		}
	}
	if custom != "" {
		return custom // what the human chose beats what the model guessed
	}
	return ai
}

// bridgeSessionID is a stable identifier for this session, used as the
// session_id of last resort so reattach-by-session works for harnesses that
// expose no session identifier of their own.
//
// It names the PARENT process, not this one, and that is the whole point.
//
// A session id is only worth anything if BOTH halves of Dibs can say it: the
// bridge, which registers the agent, and the harness plugin, which later asks
// "may this session write here?" on the agent's behalf. A random per-bridge id
// satisfies only the first half. opencode's plugin knows opencode's own session
// id and nothing about the bridge, so the two never matched: the agent went in
// under one name and every hook asked about another. That silently disabled the
// wake path and, worse, the claim guard: a hook that cannot name an agent gets
// allow, so an agent walked straight through a peer's exclusive claim and
// clobbered the file. Measured, not theorised.
//
// The parent pid is the fix because it is genuinely observed on both sides.
// Harnesses spawn the stdio bridge as a direct child, so os.Getppid() here is
// the harness's own process id: the same number its in-process plugin reads
// from process.pid. Verified against a live opencode run: bridge 22101, parent
// 22071, opencode 22071. No handshake, no negotiation, no shared file.
//
// This drops the random suffix that used to guard against PID recycling, and
// that is a deliberate trade. Recycling can only mislead if the OS hands a new
// harness the exact pid of a dead one AND the agent inside it registers the
// same agent NAME (reattach keys on both): in which case treating it as the
// same agent is very likely what the human meant anyway. An id nobody else can
// pronounce is worse than one that collides once in a blue moon.
//
// Whoever spawned an orphan is not a session: ppid 1 (or 0) would make every
// reparented bridge on the machine claim one identity, so those fall back to
// this process plus randomness: unshareable, but at least not shared WRONGLY.
var bridgeSession struct {
	sync.Once
	id string
}

func bridgeSessionID() string {
	bridgeSession.Do(func() {
		if ppid := os.Getppid(); ppid > 1 {
			bridgeSession.id = fmt.Sprintf("host-%d", ppid)
			return
		}
		var b [8]byte
		if _, err := rand.Read(b[:]); err != nil {
			// Failing closed here would break registration entirely; a
			// PID-only id is still correct within a single boot.
			bridgeSession.id = fmt.Sprintf("bridge-%d", os.Getpid())
			return
		}
		bridgeSession.id = fmt.Sprintf("bridge-%d-%s", os.Getpid(), hex.EncodeToString(b[:]))
	})
	return bridgeSession.id
}

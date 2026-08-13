package liveness

import (
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Agent is an agent-shaped process found running on this machine.
type Agent struct {
	PID     int
	PPID    int
	Harness string // "codex", "claude", "opencode", "pi", which tool it is
	Owner   string // the parent session it belongs to, "" when unattributable
	Via     string // how Owner was established, so a wrong answer can be traced
	Cmd     string // the full command line, for a human deciding what to do
}

// Discover finds agent processes and works out whose they are.
//
// # Why this is not simply PPID
//
// The obvious answer is process ancestry: a subagent's parent is its parent.
// That fails on the case that matters. Measured on a live `codex exec` spawned
// by a Claude Desktop session:
//
//	PID 16414  PPID 1  PGID 16410
//
// PPID 1. The child was launched detached, which is what every harness does
// when it wants the subagent to outlive a tool call, so the kernel reparented
// it to launchd the moment its spawner returned. The ancestry link that would
// have identified the owner is destroyed by exactly the spawning pattern that
// creates the problem.
//
// # What survives
//
// The environment does. It is inherited at fork, it survives reparenting,
// daemonisation and process-group changes, and it cannot be lost without the
// child explicitly clearing it. The same measured process still carried:
//
//	CLAUDE_CODE_ENTRYPOINT=claude-desktop
//	PATH=...  /Claude/local-agent-mode-sessions/<workspace>/<session>/rpm/...
//
// The session UUID in that path belonged to the Claude session that spawned it,
// and NOT to the sibling session running on the same machine: a natural
// experiment that shows the environment distinguishes two agents of the same
// harness, which is the whole difficulty.
//
// So attribution is a ladder, most trustworthy first, and every answer records
// which rung it came from. A wrong owner is worse than none: it would send a
// stall report to an agent that cannot act on it while the one that can hears
// nothing.
func Discover() []Agent {
	var found []Agent
	for _, p := range listProcesses() {
		h := HarnessOf(p.cmd)
		if h == "" {
			continue
		}
		a := Agent{PID: p.pid, PPID: p.ppid, Harness: h, Cmd: p.cmd}
		a.Owner, a.Via = attribute(p.pid, p.ppid)
		found = append(found, a)
	}
	return found
}

// attribute walks the ladder for one process.
func attribute(pid, ppid int) (owner, via string) {
	env := EnvironOf(pid)
	// 1. An explicit marker beats every inference. A harness hook, or a parent
	//    that cares, sets this once and every descendant inherits it.
	if m := agentsParent.FindStringSubmatch(env); len(m) == 2 {
		return m[1], "env"
	}
	// 2. The harness's own session identity, however it leaks it. Dibs already
	//    binds agents to harness session ids (bind_session), so this maps
	//    straight onto an agent with nothing new to store.
	if m := explicitSession.FindStringSubmatch(env); len(m) == 2 {
		return m[1], "session"
	}
	if m := claudeSession.FindStringSubmatch(env); len(m) == 2 {
		return m[1], "session-path"
	}
	// 3. Ancestry, which works only while the child is still a descendant,
	//    true for a tool call that blocks, false for anything detached.
	if ppid > 1 {
		if h := HarnessOf(commandOf(ppid)); h != "" {
			return strconv.Itoa(ppid), "ppid"
		}
	}
	return "", ""
}

// claudeSession matches the per-session directory Claude Desktop puts on a
// child's PATH. Incidental rather than a documented interface, so it is one
// rung of a ladder and never the only one: if the layout changes this returns
// nothing and attribution falls through, rather than returning a wrong owner.
var claudeSession = regexp.MustCompile(`local-agent-mode-sessions/[0-9a-f-]{36}/([0-9a-f-]{36})`)

// HarnessOf names the agent tool a command line runs, or "" if it is not one.
//
// Matched on the executable's basename rather than anywhere in the string: a
// command that merely MENTIONS codex, a grep, an editor, this very sweep, is
// not an agent, and treating it as one would fill a board with phantoms.
func HarnessOf(cmd string) string {
	if cmd == "" {
		return ""
	}
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return ""
	}
	switch base := filepath.Base(fields[0]); base {
	case "codex":
		// The desktop app ships a `codex` binary that is the app itself, not a
		// headless run. Only the exec/agent subcommands are subagents.
		if len(fields) > 1 && (fields[1] == "exec" || fields[1] == "app-server") {
			return "codex"
		}
		return ""
	case "claude":
		return "claude"
	case "opencode":
		return "opencode"
	case "pi":
		return "pi"
	}
	return ""
}

type rawProc struct {
	pid, ppid int
	cmd       string
}

// listProcesses enumerates this user's processes.
//
// One `ps` call for the whole table rather than a syscall per process: it is
// the same interface on macOS and Linux, needs no cgo and no new dependency,
// and costs a single fork on a cadence measured in seconds. macOS has no /proc,
// so the alternative is sysctl(KERN_PROCARGS2) through x/sys: meaningfully
// faster, and worth doing only if this ever runs often enough to notice.
func listProcesses() []rawProc {
	out, err := exec.Command("ps", "-axo", "pid=,ppid=,command=").Output()
	if err != nil {
		return nil
	}
	var procs []rawProc
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		pid, err1 := strconv.Atoi(f[0])
		ppid, err2 := strconv.Atoi(f[1])
		if err1 != nil || err2 != nil {
			continue
		}
		procs = append(procs, rawProc{pid: pid, ppid: ppid, cmd: strings.Join(f[2:], " ")})
	}
	return procs
}

// commandOf reads one process's command line.
func commandOf(pid int) string {
	if pid <= 0 {
		return ""
	}
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "command=").Output() // #nosec G204 -- pid is an int
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// EnvironOf reads a process's environment as raw text.
//
// Deliberately NOT parsed into key/value pairs. `ps eww` separates variables
// with spaces and does not quote values, so any value CONTAINING a space is
// indistinguishable from the start of the next variable, and on macOS the one
// value that matters here is PATH, which contains
// "/Library/Application Support/...". Splitting on whitespace truncated it at
// the first space and threw away the session id sitting further along, which
// made the primary attribution rung silently return nothing. It looked like
// "this harness does not expose a session", and it was a parser bug.
//
// So the blob is searched with patterns instead. Every value read here is a
// path or an id, and a pattern that fails to match yields no owner rather than
// a wrong one.
//
// Only works for processes this user owns, which is the correct limit: an agent
// belonging to somebody else is not this daemon's to attribute.
//
// macOS additionally hides the environment of Apple-signed PLATFORM binaries,
// /bin/sleep, /bin/bash, and anything they exec. That is not a limitation in
// practice: every agent harness is a user-installed binary, and a harness
// launched THROUGH a shell still works, because the shell hides its own
// environment while the agent it execs shows the variable it inherited. Both
// halves are asserted in environ_test.go against a user-compiled binary.
func EnvironOf(pid int) string {
	if pid <= 0 {
		return ""
	}
	// #nosec G204 -- pid is an int, so no shell metacharacter can reach argv
	out, err := exec.Command("ps", "eww", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return ""
	}
	return string(out)
}

// agentsParent matches the explicit marker. Its value is an agent id, which is
// ASCII and space-free by construction, so \S+ is exact rather than hopeful.
var agentsParent = regexp.MustCompile(`\bDIBS_PARENT=(\S+)`)

// explicitSession matches the session variables a harness may export directly.
var explicitSession = regexp.MustCompile(`\b(?:CLAUDE|CODEX|OPENCODE)_SESSION_ID=(\S+)`)

// resumeSession matches the session id a codex transcript carries in its name:
// rollout-<timestamp>-<uuid>.jsonl.
var resumeSession = regexp.MustCompile(`rollout-[0-9T:-]+?-([0-9a-f]{8}-[0-9a-f-]{27})\.jsonl$`)

// ResumeCommand returns the command that would restart a stalled agent where it
// stopped, or "" when there is none to offer.
//
// Dibs does not run it. That is not squeamishness: the parent knows what the
// child was for and whether re-running it is safe, and a supervisor that
// silently repairs things teaches its operator nothing while hiding a failure
// that may be systematic. But withholding the command is a different thing from
// declining to run it: a parent told "your subagent is stuck" and left to work
// out the incantation is being given a problem instead of a decision.
//
// Only codex exposes one today. Its transcript filename carries the session id,
// so the command is derivable from a path Dibs already holds; nothing new is
// stored and nothing is asked of the child.
func ResumeCommand(harness, transcript string) string {
	if harness != "codex" || transcript == "" {
		return ""
	}
	m := resumeSession.FindStringSubmatch(filepath.Base(transcript))
	if len(m) != 2 {
		return ""
	}
	return "codex exec resume " + m[1]
}

// SessionPathRungWorking counts processes currently attributed by the
// session-path rung.
//
// That rung reads a per-session directory Claude Desktop puts on a child's
// PATH: its private business, not an interface anybody published. If the
// layout changes it simply stops matching, and attribution falls to a weaker
// rung with no error and no log: the exact silent degradation this design is
// arranged to avoid elsewhere. Counting it turns "still works" into something
// observable rather than assumed.
//
// Zero is not a failure. It means no process on this machine currently needs
// that rung: every agent was either stamped (the deterministic path) or is not
// running at all.
func SessionPathRungWorking() int {
	n := 0
	for _, a := range Discover() {
		if a.Via == "session-path" {
			n++
		}
	}
	return n
}

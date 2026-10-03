// Package harnessenv says which app an agent actually runs in, from the
// process tree rather than from anything the agent says, and how to reach a
// thread inside that app.
//
// WHY IT EXISTS. An agent belongs to the environment it started in, and Dibs
// may wake it there and nowhere else. One that started in the ChatGPT app is
// woken IN the ChatGPT app, launching the app if it has to; running it anywhere
// else (a headless Codex, another harness) is a RELOCATION, which is never a
// wake and needs a permission. The operator drew that line after finding their
// ChatGPT threads running in a headless Codex that Dibs had started.
//
// So the environment decides where a wake may go, and a value that decides
// that cannot come from the agent: the same reason the host id is derived and
// never taken from what an agent states. The stdio bridge is a child of the
// harness that spawned it, so its ancestry names the app, and nothing the model
// says can change it.
package harnessenv

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// The environments Dibs can reach a thread inside. Values are what
// core.AgentInfo.Surface carries; ClaudeDesktop matches the value the Claude
// Code session sidecar has always supplied as its entrypoint.
const (
	ChatGPTApp    = "chatgpt-app"
	ClaudeDesktop = "claude-desktop"
	// CodexOutsideApp is a Codex that is not under the ChatGPT app: a
	// terminal, an IDE, `codex exec`. Not an app Dibs opens anything in; it is
	// stated so that a thread that moved out of the app stops reading as one
	// that lives there.
	CodexOutsideApp = "codex"
)

// Classify names the app a process tree belongs to, from its ancestors'
// executable paths, nearest first. "" means none Dibs knows how to reach a
// thread inside, and is the safe answer: a wake then never opens an app on the
// strength of a guess.
func Classify(ancestors []string) string {
	for _, p := range ancestors {
		switch {
		case strings.Contains(p, "/ChatGPT.app/"):
			return ChatGPTApp
		case strings.Contains(p, "/Claude.app/"),
			strings.Contains(p, "/Application Support/Claude/claude-code/"):
			return ClaudeDesktop
		}
	}
	return ""
}

// Detect classifies the process tree this process is running in.
func Detect(self int) string {
	return Classify(Ancestors(self, 6))
}

// Ancestors lists the executable paths of pid's ancestors, nearest first,
// stopping at launchd or after depth steps. Errors end the walk: an ancestry we
// cannot read classifies as unknown, never as an app.
func Ancestors(pid, depth int) []string {
	var out []string
	for i := 0; i < depth && pid > 1; i++ {
		//nolint:gosec // a fixed binary and a pid formatted from an int
		raw, err := exec.Command("/bin/ps", "-o", "ppid=,comm=", "-p", strconv.Itoa(pid)).Output()
		if err != nil {
			return out
		}
		fields := strings.Fields(string(raw))
		if len(fields) < 2 {
			return out
		}
		parent, err := strconv.Atoi(fields[0])
		if err != nil || parent <= 1 {
			return out
		}
		//nolint:gosec // a fixed binary and a pid formatted from an int
		comm, err := exec.Command("/bin/ps", "-o", "comm=", "-p", strconv.Itoa(parent)).Output()
		if err != nil {
			return out
		}
		out = append(out, strings.TrimSpace(string(comm)))
		pid = parent
	}
	return out
}

// chatGPTRoot is where the ChatGPT app lives; its own Codex runtime runs from
// inside it, and is what holds a loaded thread's transcript open.
const chatGPTRoot = "/Applications/ChatGPT.app/"

// ChatGPTHolds reports whether the running ChatGPT app has this thread loaded,
// which is when a message queued for it is delivered at once and the app needs
// nothing more. Measured: the app's own Codex runtime keeps a loaded thread's
// rollout file open, and does not hold one it has not loaded (an app update
// released every thread; opening one loaded it within a second).
//
// running is false when no process from the app is up at all, which is the
// case where opening the thread has to launch the app.
func ChatGPTHolds(thread string) (running, holds bool) {
	if thread == "" {
		return false, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	out, err := appProbeOutput(ctx, "/bin/ps", "-axo", "pid=,comm=")
	if err != nil {
		return false, false // process inventory unavailable: no ownership evidence
	}
	var pids []string
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || !strings.HasPrefix(strings.Join(fields[1:], " "), chatGPTRoot) {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err == nil && pid > 0 {
			pids = append(pids, strconv.Itoa(pid))
		}
	}
	if len(pids) == 0 {
		return false, false
	}
	// One process-wide query: helpers must not exhaust the shared deadline
	// before the runtime holding the thread is reached.
	files, _ := appProbeOutput(ctx, "/usr/sbin/lsof", "-p", strings.Join(pids, ","), "-Fn")
	// lsof may report a vanished helper with a nonzero exit while returning
	// valid files for the surviving runtime. Only a file is ownership evidence.
	for _, line := range strings.Split(string(files), "\n") {
		if strings.HasPrefix(line, "n") && strings.Contains(line[1:], thread) {
			return true, true
		}
	}
	return true, false
}

// Fixed binaries only. The seam lets a fixture block the real API's probe
// without reaching the operator's app or replacing the timeout policy.
var appProbeOutput = func(ctx context.Context, binary string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, binary, args...).Output() //nolint:gosec // fixed probe argv above
}

// ChatGPTOpenArgv is the command that asks the ChatGPT app to open a thread:
// the app's own codex:// route, which loads the thread in the app's runtime and
// brings it to the front, launching the app first if it is not running. That is
// the thread waking in the app it was started in, which is the whole of what a
// wake may do. Measured: an unloaded thread was held by the app within a second.
//
// nil for a thread id that is not one: the id is the agent's to state, and it
// becomes part of a URL, so anything but the characters a thread id is made of
// is refused rather than escaped.
func ChatGPTOpenArgv(thread string) []string {
	if !isThreadID(thread) {
		return nil
	}
	return []string{"/usr/bin/open", "codex://threads/" + thread}
}

func isThreadID(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	for _, r := range s {
		ok := r == '-' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		if !ok {
			return false
		}
	}
	return true
}

// OnlyDerived reports whether surface is one a wake acts on, and so one that
// only a bridge's reading of the process tree may set. An agent stating it
// about itself is ignored: otherwise a terminal Codex could name the app and
// have Dibs open its thread there, which is the relocation this package exists
// to prevent.
func OnlyDerived(surface string) bool { return surface == ChatGPTApp }

// OpenArgv is the command that opens thread inside the app named by surface,
// or nil when that is not an app Dibs knows how to reach a thread inside. It is
// the ONLY place a wake learns how to open an app, so an agent that did not
// start in an app is never opened in one.
func OpenArgv(surface, thread string) []string {
	switch surface {
	case ChatGPTApp:
		return ChatGPTOpenArgv(thread)
	case ClaudeDesktop:
		return ClaudeOpenArgv(ClaudeLocalSession(thread)) // reads the app's records: off the writer loop
	}
	return nil
}

// Shower makes sure a thread a message was just queued for is loaded in its
// app, so the app delivers the message to the agent the operator can see. Its
// two contacts with the real app are fields so a test can stand in for them: a
// test that reached the real `open codex://...` would switch the operator's
// ChatGPT window every time the suite ran.
type Shower struct {
	Holds func(thread string) bool
	Open  func(argv []string) error
	// Idle, MinIdle, Wait and Poll defer an open until the person has been
	// away for MinIdle (see idle.go). A nil Idle or a zero MinIdle opens at
	// once.
	Idle    func() (time.Duration, bool)
	MinIdle time.Duration
	Wait    func(time.Duration)
	Poll    time.Duration
}

// RealShower reaches the real app.
var RealShower = Shower{
	// Either app: thread ids are UUIDs, so one cannot name the other's.
	Holds: func(thread string) bool {
		if ClaudeSessionRunning(thread) {
			return true
		}
		_, holds := ChatGPTHolds(thread)
		return holds
	},
	Open: func(argv []string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return exec.CommandContext(ctx, argv[0], argv[1:]...).Run() //nolint:gosec // argv is OpenArgv's, never text from mail
	},
	Idle:    UserIdle,
	MinIdle: DefaultOpenAfterIdle,
	Wait:    func(d time.Duration) { <-time.After(d) },
}

// Show opens the thread in its app when the app is not already holding it.
// Opening brings the app to the front, so a thread the app already has loaded
// is left alone: the queue delivers to it without help. opened reports whether
// it asked the app to open the thread.
func (s Shower) Show(argv []string, thread string) (opened bool, err error) {
	if len(argv) == 0 || s.Holds(thread) {
		return false, nil
	}
	if err := s.Open(argv); err != nil {
		return false, err
	}
	return true, nil
}

// NeedsNoWakeCommand reports the harnesses reached without a [wake.exec]
// entry, by their lowercased name as an agent reports it. Claude Code delivers
// through its session socket and its hooks, so an agent on it is not "missing
// a wake command", and nothing should tell a sender or an operator that it is.
// One place, read by doctor and by the daemon's notes to senders.
func NeedsNoWakeCommand(harness string) bool { return harness == "claude code" }

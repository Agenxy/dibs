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
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/notify"
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
	state := ChatGPTOwnership(thread)
	return state.Running, state.Loaded
}

// ThreadOwnership keeps a failed observation distinct from an unloaded thread.
// Epoch is the app's process incarnation, never a message or model turn.
type ThreadOwnership struct {
	Running, Known, Loaded bool
	Epoch                  string
}

// ChatGPTOwnership probes only the bundled Codex runtimes, not the app's many
// renderer and tool helpers. The native app tool's notLoaded view is not a
// public endpoint dibd can call; spawning another app-server would inspect the
// wrong runtime. A missing/changed probe therefore remains UNKNOWN.
func ChatGPTOwnership(thread string) ThreadOwnership {
	if thread == "" {
		return ThreadOwnership{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	out, err := appProbeOutput(ctx, "/bin/ps", "-axo", "pid=,lstart=,comm=")
	if err != nil || len(out) > 4<<20 {
		return ThreadOwnership{}
	}
	state, pids := appProcessEvidence(string(out))
	if !state.Running {
		return state
	}
	if len(pids) == 0 {
		return state // app present, runtime inventory unknown
	}
	files, err := appProbeOutput(ctx, "/usr/sbin/lsof", "-a", "-p", strings.Join(pids, ","), "-Fn")
	// lsof may report a vanished helper with a nonzero exit while returning
	// valid files for the surviving runtime. Only a file is ownership evidence.
	for _, line := range strings.Split(string(files), "\n") {
		if strings.HasPrefix(line, "n") && strings.Contains(line[1:], thread) {
			state.Known, state.Loaded = true, true
			return state
		}
	}
	state.Known = err == nil && ctx.Err() == nil
	return state
}

func appProcessEvidence(raw string) (ThreadOwnership, []string) {
	var pids, epochs []string
	var appEpoch string
	running := false
	for _, line := range strings.Split(raw, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 7 {
			continue
		}
		path := strings.Join(fields[6:], " ")
		if !strings.HasPrefix(path, chatGPTRoot) {
			continue
		}
		running = true
		pid, err := strconv.Atoi(fields[0])
		if err != nil || pid <= 0 {
			continue
		}
		epoch := fields[0] + ":" + strings.Join(fields[1:6], " ")
		if path == chatGPTRoot+"Contents/MacOS/ChatGPT" {
			appEpoch = epoch
		}
		if filepath.Base(path) == "codex" {
			pids = append(pids, strconv.Itoa(pid))
			epochs = append(epochs, epoch)
		}
	}
	if !running {
		return ThreadOwnership{Known: true, Epoch: "absent"}, nil
	}
	if appEpoch == "" {
		sort.Strings(epochs)
		appEpoch = strings.Join(epochs, ",")
	}
	return ThreadOwnership{Running: true, Epoch: appEpoch}, pids
}

// Fixed binaries only. The seam lets a fixture block the real API's probe
// without reaching the operator's app or replacing the timeout policy.
var appProbeOutput = func(ctx context.Context, binary string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, binary, args...) //nolint:gosec // fixed probe argv above
	// Process start stamps must agree across bridges and the daemon even when
	// their inherited locales or timezones differ. No participant-supplied environment.
	cmd.Env = append(os.Environ(), "LC_ALL=C", "TZ=UTC")
	return cmd.Output()
}

// ChatGPTOpenArgv is the app's thread URL for an explicit relocation. Wakes
// add the background option through chatGPTWakeArgv below. Both use the app's
// own runtime, launching it if needed, never a headless agent process.
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

func chatGPTWakeArgv(thread string) []string {
	argv := ChatGPTOpenArgv(thread)
	if argv == nil {
		return nil
	}
	return []string{argv[0], "-g", argv[1]}
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
		return chatGPTWakeArgv(thread)
	case ClaudeDesktop:
		return ClaudeOpenArgv(ClaudeLocalSession(thread)) // reads the app's records: off the writer loop
	}
	return nil
}

// Shower makes sure a thread a message was just queued for is loaded in its
// app, so the app delivers the message to the agent the operator can see. Its
// contacts with the real app are fields so a test can stand in for them: a
// test that reached the real `open codex://...` would switch the operator's
// ChatGPT window every time the suite ran.
type Shower struct {
	Holds func(thread string) bool
	Open  func(argv []string) error
	// Ownership is the production tri-state probe. Holds is the test seam and
	// takes precedence when supplied; false there is positive unloaded evidence.
	Ownership func(thread string) ThreadOwnership
	// Claude recovery only: Away observes lock/display sleep. Idle and MinIdle permit an AFK open;
	// both observations fail closed in production. Open also rechecks these
	// facts natively and restores the prior app. Wait/Poll drive deferral.
	Away    func() (bool, bool)
	Idle    func() (time.Duration, bool)
	MinIdle time.Duration
	Wait    func(time.Duration)
	Poll    time.Duration
}

// appOpenFixture replaces only the native contact, keeping RealShower's
// production route selector in the test. Nil is the only production value.
// Unlike an environment switch, this cannot turn a built child into a fake.
var appOpenFixture func(mode, url string, minIdle time.Duration) error

// RealShower reaches the real app.
var RealShower = Shower{
	Ownership: ChatGPTOwnership,
	Open: func(argv []string) error {
		// A package may copy RealShower before its TestMain installs a fake.
		// Fail the test, even if its caller discards the returned error, rather
		// than opening the person's app from a test subprocess.
		if appOpenFixture == nil && testing.Testing() {
			panic("unfaked real app opener in a Go test")
		}
		// TestMain passes this to built helper binaries, where
		// testing.Testing() is false. A child must still never open the app.
		if appOpenFixture == nil && os.Getenv("DIBS_TEST_FORBID_APP_OPEN") == "1" {
			return errors.New("real app opener forbidden by DIBS_TEST_FORBID_APP_OPEN")
		}
		if len(argv) != 3 || argv[0] != "/usr/bin/open" {
			return errors.New("unsupported native app open")
		}
		if argv[1] == "-g" && strings.HasPrefix(argv[2], "codex://threads/") &&
			isThreadID(strings.TrimPrefix(argv[2], "codex://threads/")) {
			if appOpenFixture != nil {
				return appOpenFixture("chatgpt-background", argv[2], 0)
			}
			return notify.OpenChatGPTBackground(argv[2])
		}
		seconds, err := strconv.ParseFloat(argv[2], 64)
		if err != nil {
			return err
		}
		if appOpenFixture != nil {
			return appOpenFixture("claude-away", argv[1], time.Duration(seconds*float64(time.Second)))
		}
		return notify.OpenWhenAway(argv[1], time.Duration(seconds*float64(time.Second)))
	},
	Away: func() (bool, bool) {
		desk, err := notify.DesktopState()
		return desk.Away(), err == nil
	},
	Idle:    UserIdle,
	MinIdle: DefaultOpenAfterIdle,
	Wait:    func(d time.Duration) { <-time.After(d) },
}

// Show opens the thread in its app when the app is not already holding it.
// Opening may briefly activate the app despite -g, so a thread it already has loaded
// is left alone: the queue delivers to it without help. opened reports whether
// it asked the app to open the thread.
func (s Shower) Show(argv []string, thread string) (opened bool, err error) {
	if isChatGPTOpen(argv) {
		return s.showChatGPT(argv, thread)
	}
	if len(argv) == 0 || s.holds(thread) {
		return false, nil
	}
	if s.Away != nil {
		argv = append(append([]string(nil), argv...), strconv.FormatFloat(s.MinIdle.Seconds(), 'f', -1, 64))
	}
	if err := s.Open(argv); err != nil {
		return false, err
	}
	return true, nil
}

func (s Shower) holds(thread string) bool {
	if s.Holds != nil {
		return s.Holds(thread)
	}
	return ClaudeSessionRunning(thread)
}

// NeedsNoWakeCommand reports the harnesses reached without a [wake.exec]
// entry, by their lowercased name as an agent reports it. Claude Code delivers
// through its session socket and its hooks, so an agent on it is not "missing
// a wake command", and nothing should tell a sender or an operator that it is.
// One place, read by doctor and by the daemon's notes to senders.
func NeedsNoWakeCommand(harness string) bool { return harness == "claude code" }

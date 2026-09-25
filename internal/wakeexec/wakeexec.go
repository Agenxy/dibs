// Package wakeexec runs an operator's wake command: the substitutions a
// [wake.exec] entry may take, the process itself, its bounds, and the fallback
// rule. Shared by the daemon, which runs wakes for agents on its own machine,
// and by `dibs host-bridge`, which runs them for agents on this machine when
// the hub is elsewhere (docs/NETWORK.md §5). One implementation, so the two
// cannot drift apart on what a command is handed or when its fallback runs.
//
// Everything here was written in the daemon's waker and moved, comments
// included: each sentence below was paid for by a defect there.
package wakeexec

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Compose writes what a wake says on the route that cannot carry mail.
//
// THERE IS NO FIXED SENTENCE ANY MORE, and removing it is the correction.
// This used to be one constant, "Dibs: check the board.", carried by every
// wake on every route. Two years of reasoning went into that sentence and all
// of it was about what a wake must not do: not steer, not name tools in
// order, not claim "you have mail" when a durable wake might land after the
// mail was read. Every one of those arguments is sound and none of them
// required an IMPERATIVE. "Check the board" tells an agent to go and look;
// what it is allowed to do is tell an agent what happened.
//
// The operator saw it arrive beside a notice that already carried the whole
// message, asked what it was for three times across two weeks, and was right
// every time. The honest answer was "nothing".
//
// So this states a fact and stops. `from` and `msgType` are already
// substituted into the same argv as their own fields, so naming them here
// leaks nothing that route did not already carry, and the BODY is still never
// here: argv is world-readable through `ps` and that rule has not moved.
//
// Composed rather than stored, which is what keeps the machine boundary
// honest. `dibs host-bridge` builds this line locally from the structured
// fields a hub sent it, so a hub still cannot compose text for a command that
// delivers text into a harness (PHILOSOPHY rule 5). It sends facts; the
// machine that runs the command writes the sentence.
// NO NAMES IN IT, and that is a rule rather than a style. The agent id and
// the sender are already substituted into this argv as their OWN elements, so
// repeating them inside the message would duplicate them in the one form that
// is not a whole element. A name is chosen by whoever registers, so it is
// attacker-influenced, and `TestAMessageCannotInfluenceWhatTheWakeCommandRuns`
// exists because pasting such a value into a larger string is where quoting
// bugs live even when there is no shell to blame. The first version of this
// function did exactly that and produced
// `Dibs: new request from "; rm -rf / #" for your agent "; rm -rf / #".`
// as a single argv element. The test caught it; the reasoning is older than
// the test.
//
// The socket route has no such constraint, because its payload is JSON on a
// 0600 endpoint rather than argv, so it names the sender itself.
func Compose(msgType string) string {
	if msgType != "" {
		return fmt.Sprintf("Dibs: a new %s is waiting.", msgType)
	}
	return "Dibs: something is waiting."
}

// Fields are the only substitutions a wake command gets.
//
// Each replaces a WHOLE argv element, never part of one, and the value is
// passed to exec as a single argument. There is no shell anywhere in this path,
// so a message body containing a semicolon is a message body containing a
// semicolon.
type Fields struct {
	// thread is the identifier the harness's own resume command accepts,
	// which is NOT the agent's session_id: that one names the harness
	// PROCESS ("host-92368") and no resume command has ever heard of it.
	// The engine finds this; when it finds nothing, nothing is woken.
	Thread  string
	Agent   string
	From    string
	MsgType string
	Message string
}

// Apply substitutes the fields into a copy of argv.
func (f Fields) Apply(argv []string) []string {
	out := make([]string, 0, len(argv))
	for _, a := range argv {
		switch a {
		case "{thread}":
			out = append(out, f.Thread)
		case "{agent}":
			out = append(out, f.Agent)
		case "{from}":
			out = append(out, f.From)
		case "{type}":
			out = append(out, f.MsgType)
		case "{message}":
			out = append(out, f.Message)
		default:
			out = append(out, a)
		}
	}
	return out
}

// Timeout is the longest a wake command may run before it is killed.
//
// This was 30 seconds, which is a sensible bound for a notification and a
// catastrophic one for the command actually documented: `codex exec resume`
// continues the thread IN THIS PROCESS, so the timeout is a cap on the agent's
// whole turn. An ordinary turn passes 30s easily, and the kill landed mid-work
// with the cooldown already spent and no retry, so the wake destroyed the
// activation it had just created and the blocking message stayed unread. The
// bound exists only to stop a wedged process living forever; it must be far
// past any turn a person would wait for, and this is.
const Timeout = 2 * time.Hour

// Grace bounds the wait AFTER the deadline kills the command.
//
// cmd.WaitDelay, and it has to be set because stdout and stderr are not files.
// For a non-file writer os/exec copies through a pipe, and killing the process
// at the deadline does not close descriptors a GRANDCHILD inherited: Wait then
// blocks on EOF that never comes, forever, well past the two-hour bound this
// package advertises. `codex exec resume` starting a helper that outlives it is
// an ordinary thing for a wake command to do.
//
// The consequence was worse than a stuck goroutine. wakeFinished runs on defer,
// so it never ran, wakers.running kept that agent marked as still going, and
// every later message to it was refused as a duplicate: one leaked descriptor
// made an agent permanently unreachable until the daemon restarted. Two fixes
// of mine met, the tail buffer and the running map, and neither was wrong
// alone.
const Grace = 10 * time.Second

// RunCommands runs the operator's command, and the fallback only if the
// first one fails.
//
// The primary's failure is still logged in full by runWakeFor, argv and
// directory included, because an operator whose primary is failing on every
// wake wants to know that even while the fallback is carrying the load. What
// follows says whether anything was tried next, so the two lines read as one
// story rather than a failure and an unexplained success.
//
// Measured, both halves, on the board this was written for: `codex exec
// resume` exit 1 with "already has an active writer" on a thread open in the
// desktop app, then `codex queue` exit 0, then the thread's own transcript
// carrying "Dibs: check the board." and the agent answering two questions it
// had been sent. The reverse case, a closed thread, is the one the primary
// already handled.
func RunCommands(argv, fallback []string, agent, dir string, timeout, grace time.Duration) bool {
	ok, out := runForOut(argv, agent, dir, timeout, grace)
	if ok {
		return true
	}
	if len(fallback) == 0 {
		return false
	}
	// ONLY WHEN THE THREAD IS OPEN. The fallback exists for one failure: the
	// harness refusing to resume a thread its desktop app holds open. Run
	// after ANY failure, `codex queue` exited 0 on a closed thread whose
	// resume had failed for some other reason, parking the message where
	// nothing reads it, and that counted as a wake and suppressed the retry.
	// The primary's own words decide. Found by the pre-release review, round
	// twenty-three.
	if !openThreadFailure(out) {
		slog.Info("the wake command failed for a reason that is not an open thread; "+
			"the fallback would park the message, so it does not run",
			"agent", agent, "cmd", argv[0], "run_it_yourself", strings.Join(argv, " "))
		return false
	}
	slog.Info("the wake command found the thread open; trying the fallback",
		"agent", agent, "cmd", argv[0], "fallback", fallback[0])
	return Run(fallback, agent, dir, timeout, grace)
}

// openThreadMarkers are what the measured harness prints when it refuses to
// resume a thread something else holds open (codex 0.153: "thread-store
// conflict: thread <id> already has an active writer").
var openThreadMarkers = []string{"active writer", "thread-store conflict"}

func openThreadFailure(out []byte) bool {
	text := strings.ToLower(string(out))
	for _, m := range openThreadMarkers {
		if strings.Contains(text, m) {
			return true
		}
	}
	return false
}

// runForOut is the run with its bounds as arguments, so a test can assert that
// this RETURNS rather than assert that a constant is large. The old test
// checked only that wakeTimeout was at least two hours, which stays true while
// Wait blocks past it.
func runForOut(argv []string, agent, dir string, timeout, grace time.Duration) (bool, []byte) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	// #nosec G204 -- argv comes from the operator's own config file and nowhere
	// else: SetWakeCommands is the only writer, no tool or op reaches it, and
	// substitution replaces whole elements rather than building a string. There
	// is no shell in this path.
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	// IN THE AGENT'S DIRECTORY, WHICH IS THE WHOLE REASON THIS EVER WORKED.
	//
	// Without this the command inherits the DAEMON'S working directory, and a
	// daemon started by launchd has "/". Both documented wake commands care:
	// `codex exec resume` refuses outright there ("Not inside a trusted
	// directory"), exit 1, which is precisely the failure in this repository's
	// own daemon log, three times; and an agent resumed anywhere else would run
	// its next turn in the wrong tree even where the harness tolerates it.
	//
	// The plan has carried this value since the path shipped, set from the
	// agent's own record and commented as what it was for, and nothing read it.
	// That is worse than never having had it: the mechanism looked finished.
	//
	// Empty, or gone since the agent registered, means run where the daemon is
	// rather than refuse. A wake that might work beats one that certainly does
	// not, and the log says which happened so a failure is not a mystery.
	if dir != "" {
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			cmd.Dir = dir
		} else {
			slog.Warn("the agent's directory is gone, so its wake runs where the "+
				"daemon does; a harness that resolves sessions per directory will "+
				"not find this one",
				"agent", agent, "cwd", dir, "err", err)
		}
	}
	// BOUNDED. CombinedOutput holds every byte until the process exits, and the
	// documented command is `codex exec resume`, which runs a whole agent turn
	// and may print a transcript for two hours. All of it sat in the daemon's
	// memory and was then thrown away on success. Only the tail is ever used:
	// it goes in the warning when the command fails, and a wake that failed says
	// why in its last few lines rather than its first thousand.
	tail := &tailBuffer{limit: 8 << 10}
	cmd.Stdout, cmd.Stderr = tail, tail
	cmd.WaitDelay = grace
	err := cmd.Run()
	out := tail.Bytes()
	if err != nil {
		// WHAT THE OS SAID, not what the agent printed.
		//
		// The documented wake command runs an entire agent turn, so its stdout
		// is transcript: decrypted mail, tool output, a model's summary of a
		// private message. Logging it put all of that on stderr and in
		// /api/logs, which undoes the reason mail is encrypted at rest.
		//
		// A command that never STARTED is different. There is no agent then,
		// and the bytes are the operating system's own complaint, which is the
		// half an operator actually needs to fix a wrong argv.
		fields := []any{"agent", agent, "cmd", argv[0], "err", err}
		var ee *exec.Error
		if errors.As(err, &ee) {
			fields = append(fields, "output", strings.TrimSpace(string(out)))
		}
		// THE COMMAND, SO SOMEBODY CAN RUN IT THEMSELVES.
		//
		// The output stays withheld for the reason above, and that left
		// "exit status 1" and nothing else: an operator cannot act on that.
		// The argv is the operator's own config, so printing it discloses
		// nothing they did not write, and running it by hand is the one way to
		// see the output this deliberately will not log.
		fields = append(fields, "run_it_yourself", strings.Join(argv, " "))
		// AND WHERE IT RAN, because that is the difference that bites.
		//
		// This line used to blame the login keychain, and that was wrong. It
		// said a service cannot reach the operator's keychain or GUI session,
		// which sounded right and sent two investigations down a dead end. A
		// LaunchAgent probe in the identical domain and ProcessType as this
		// daemon read the login keychain and ran a complete `claude --resume`
		// turn, exit 0. The security session was never the problem.
		//
		// What actually differed was the working directory, now fixed above, so
		// the honest note names the directory the command really ran in and
		// leaves the diagnosis to whoever reads it.
		fields = append(fields, "ran_in", runDir(dir))
		// NO THEORY ABOUT WHY. Three have been wrong here.
		//
		// This line has carried a guess at the cause since it was written, and
		// the guess has misled every operator who read it, including the ones
		// who wrote it. First it blamed launchd's security session and the login
		// keychain, which a probe in the identical domain disproved. Then it
		// blamed the login shell's environment, and the real cause was a
		// thread-store conflict: the thread was open in the harness's desktop
		// app, which refuses a second writer, and no environment anywhere would
		// have changed that.
		//
		// The facts are useful and the theory is not. An operator has the argv
		// and the directory, which is enough to run it and see the real error in
		// under a minute; that is how the third wrong guess was caught. What
		// stays withheld is the command's OUTPUT, because a wake runs a whole
		// agent turn and that output is somebody's decrypted mail.
		if os.Getppid() == 1 {
			fields = append(fields,
				"note", "run the command above yourself to see what it said: its output "+
					"is withheld here because a wake runs a whole agent turn, and that "+
					"is somebody's decrypted mail")
		}
		slog.Warn("wake command failed; the next message somebody is blocked on "+
			"will try again", fields...)
		return false, out
	}
	slog.Info("woke an agent that was not running", "agent", agent, "cmd", argv[0])
	return true, out
}

// Run is runForOut for callers that need the verdict alone.
func Run(argv []string, agent, dir string, timeout, grace time.Duration) bool {
	ok, _ := runForOut(argv, agent, dir, timeout, grace)
	return ok
}

// tailBuffer keeps the last `limit` bytes written to it and discards the rest.
//
// A wake command is somebody else's program running for as long as an agent
// turn takes. Buffering all of it is an unbounded allocation controlled by
// whatever that program decides to print; keeping the tail is what a failure
// message actually needs.
type tailBuffer struct {
	mu    sync.Mutex
	limit int
	buf   []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := len(p)
	if len(p) > t.limit {
		p = p[len(p)-t.limit:]
	}
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - t.limit; over > 0 {
		t.buf = append(t.buf[:0], t.buf[over:]...)
	}
	return n, nil
}

func (t *tailBuffer) Bytes() []byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]byte(nil), t.buf...)
}

// runDir names the directory a wake really ran in, for the failure log.
//
// Says "the daemon's own" rather than printing it, because the useful fact is
// that it was NOT the agent's: an operator reading "/" has to know what it was
// supposed to be before that means anything.
func runDir(dir string) string {
	if dir == "" {
		return "the daemon's own working directory (the agent recorded none)"
	}
	return dir
}

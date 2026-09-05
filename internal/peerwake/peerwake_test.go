package peerwake

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// A fixture that looks exactly like what the harness writes: a key file, a
// sidecar, and a bound socket.
func fixture(t *testing.T, pid int, session, token, procStart string, bind bool) (home, sock string) {
	t.Helper()
	home = t.TempDir()
	dir := filepath.Join(home, ".claude", "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// The name shape is load-bearing: the harness reads only <pid>.<64hex>.key,
	// and so does Discover.
	name := strconv.Itoa(pid) + "." + strings.Repeat("a", 64) + ".key"
	key := map[string]any{"peerToken": token}
	if procStart != "" {
		key["procStart"] = procStart
	}
	b, _ := json.Marshal(key)
	if err := os.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil {
		t.Fatal(err)
	}
	side, _ := json.Marshal(map[string]any{"pid": pid, "sessionId": session, "cwd": "/w"})
	if err := os.WriteFile(filepath.Join(dir, strconv.Itoa(pid)+".json"), side, 0o600); err != nil {
		t.Fatal(err)
	}
	if bind {
		// A SHORT base, deliberately. A unix socket path is bounded at ~104
		// bytes by the platform, and the default temp dir on macOS is long
		// enough that binding under it fails with EINVAL. That is the same bound
		// socketFor refuses to exceed, so a fixture that ignored it would be
		// testing a path production would never produce.
		run, err := os.MkdirTemp("/tmp", "pw")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(run) })
		t.Setenv("XDG_RUNTIME_DIR", run)
		if err := os.MkdirAll(filepath.Join(run, "cc-socks"), 0o700); err != nil {
			t.Fatal(err)
		}
		sock = filepath.Join(run, "cc-socks", strconv.Itoa(pid)+".sock")
	}
	return home, sock
}

func alwaysAlive(int, string) bool { return true }

// A wake must go to the session it names, over the socket that session bound,
// authenticated, and carrying nothing but the notice.
//
// Asserted on the WIRE, because everything above this is inference until the
// bytes are read by something. The harness parses newline-delimited JSON and
// refuses a connection whose first line is not a valid auth line, so the order
// and the framing are the contract rather than an implementation detail.
func TestAWakeIsDeliveredOnTheWire(t *testing.T) {
	const pid, session, token = 4242, "19d67315-7718-491e-be3f-3864f577eeed", "768509232a525210ee1a5a6d9699de93"
	home, sock := fixture(t, pid, session, token, "", true)

	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("binding the fake session socket: %v", err)
	}
	defer func() { _ = ln.Close() }()

	got := make(chan []string, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			got <- nil
			return
		}
		defer func() { _ = c.Close() }()
		_ = c.SetDeadline(time.Now().Add(5 * time.Second))
		// READ TO EOF, not one Read. Two Writes need not arrive in one read, so
		// a single Read is a race: this test passed locally and then failed in
		// the gate having seen only the auth line. Deliver half-closes when it
		// is done, which is what makes EOF the correct end-of-message here, and
		// it is the same signal the harness itself waits on.
		b, _ := io.ReadAll(c)
		got <- strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	}()

	sessions, err := Discover(home, alwaysAlive)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("discovered %d sessions, wanted the one in the fixture", len(sessions))
	}
	if sessions[0].SessionID != session {
		t.Fatalf("discovered session %q, wanted %q", sessions[0].SessionID, session)
	}

	const notice = "Dibs: 2 unread message(s) for your agent \"dibs-dev\""
	if err := Deliver(context.Background(), sessions[0], notice); err != nil {
		t.Fatalf("deliver: %v", err)
	}

	lines := <-got
	if len(lines) != 2 {
		t.Fatalf("wrote %d line(s), wanted the auth line and the notice: %q", len(lines), lines)
	}
	var auth struct {
		Type, Token string
	}
	if err := json.Unmarshal([]byte(lines[0]), &auth); err != nil {
		t.Fatalf("the first line is not JSON: %q", lines[0])
	}
	if auth.Type != "auth" || auth.Token != token {
		t.Errorf("first line is %q; the harness refuses a connection whose first "+
			"line is not the auth line, so this must be {type:auth,token:<peerToken>}",
			lines[0])
	}
	var msg struct {
		Type    string `json:"type"`
		Message struct {
			Role, Content string
		} `json:"message"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &msg); err != nil {
		t.Fatalf("the second line is not JSON: %q", lines[1])
	}
	if msg.Type != "user" || msg.Message.Role != "user" || msg.Message.Content != notice {
		t.Errorf("second line is %q, wanted the notice as a user message", lines[1])
	}
}

// A session whose process is gone must not be discovered.
//
// A pid is reused within hours on a busy machine, and a crashed session leaves
// its key file behind. Delivering to whatever inherited the number is how a
// wake path comes to report success while reaching a stranger, which is the
// exact class this repository keeps finding in itself.
func TestADeadSessionIsNotDiscovered(t *testing.T) {
	home, _ := fixture(t, 4243, "s", "768509232a525210ee1a5a6d9699de93", "Mon Aug 24 04:42:57 2026", true)
	dead := func(int, string) bool { return false }
	sessions, err := Discover(home, dead)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 0 {
		t.Errorf("a session whose process is gone was offered as wakeable: %+v", sessions)
	}
}

// And a live pid whose START TIME disagrees is a different process wearing the
// same number, which is the case the guard exists for rather than a nicety.
func TestAPidReusedByAnotherProcessIsNotDiscovered(t *testing.T) {
	home, _ := fixture(t, 4244, "s", "768509232a525210ee1a5a6d9699de93", "Mon Aug 24 04:42:57 2026", true)
	var sawProcStart string
	reused := func(_ int, procStart string) bool {
		sawProcStart = procStart
		return false // the caller compares; here it disagrees
	}
	sessions, _ := Discover(home, reused)
	if sawProcStart == "" {
		t.Error("the start time in the key file was never offered to the liveness " +
			"check, so a recycled pid could not possibly be caught")
	}
	if len(sessions) != 0 {
		t.Errorf("a recycled pid was offered as wakeable: %+v", sessions)
	}
}

// A malformed or half-written key is refused here rather than becoming an auth
// failure against a live session.
func TestAMalformedKeyIsRefused(t *testing.T) {
	for name, token := range map[string]string{
		"truncated":   "768509232a5252",
		"not hex":     "zzzz09232a525210ee1a5a6d9699de93",
		"empty":       "",
		"over-length": "768509232a525210ee1a5a6d9699de9300",
	} {
		t.Run(name, func(t *testing.T) {
			home, _ := fixture(t, 4245, "s", token, "", true)
			sessions, _ := Discover(home, alwaysAlive)
			if len(sessions) != 0 {
				t.Errorf("a %s peer token was accepted: %+v", name, sessions)
			}
		})
	}
}

// No socket, no wake. A session that has not bound one cannot be reached, and
// saying otherwise is the silent-success failure this whole path exists to
// remove.
func TestASessionWithNoSocketIsNotDiscovered(t *testing.T) {
	home, _ := fixture(t, 4246, "s", "768509232a525210ee1a5a6d9699de93", "", false)
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir()) // exists, but nothing bound in it
	sessions, _ := Discover(home, alwaysAlive)
	if len(sessions) != 0 {
		t.Errorf("a session with no bound socket was offered as wakeable: %+v", sessions)
	}
}

// An empty notice is refused, because a wake that says nothing is a process
// interrupted for no reason.
func TestAnEmptyNoticeIsRefused(t *testing.T) {
	if err := Deliver(context.Background(), Session{Socket: "/nonexistent"}, "   "); err == nil {
		t.Error("an empty notice was accepted")
	}
}

// A machine with no Claude Code is not an error, it is a machine with no Claude
// Code. Returning one would make every board without it log a failure per wake.
func TestNoHarnessIsNotAnError(t *testing.T) {
	sessions, err := Discover(t.TempDir(), alwaysAlive)
	if err != nil {
		t.Errorf("a home with no sessions directory reported an error: %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("discovered sessions that cannot exist: %+v", sessions)
	}
}

// The socket-path bound is the platform's, not a preference, and a session
// whose path would exceed it is not ours to guess at.
//
// Encoded because the fixture above hit it first: the default temp directory on
// macOS is long enough that binding under it fails outright, so a session there
// really does live somewhere this cannot reach.
func TestAnOverlongSocketPathIsRefusedRatherThanBound(t *testing.T) {
	long := filepath.Join("/tmp", strings.Repeat("d", maxSocketPath), "x")
	t.Setenv("XDG_RUNTIME_DIR", long)
	if _, ok := socketFor(4247); ok {
		t.Errorf("a socket path over %d bytes was offered; binding one fails with "+
			"EINVAL, so offering it would turn a platform limit into a wake that "+
			"silently never arrives", maxSocketPath)
	}
}

// The liveness stamp is UTC, and comparing it against a local one rejects every
// live session on the machine.
//
// This is not a hypothetical and it is the reason this test exists. The harness
// records procStart with `LC_ALL=C TZ=UTC ps -o lstart=`. A plain `ps -o
// lstart=` answers in LOCAL time. The two agree to the second and differ by the
// machine's offset, which is indistinguishable from a recycled pid, so a naive
// comparison rejects everything and the wake path goes silent with the guard
// reporting nothing wrong. Measured: it discovered zero of six live sessions,
// while every other test in this file passed.
//
// Skipped where the machine is already on UTC, because there the two forms are
// identical and the test could not tell a correct implementation from the bug.
func TestLivenessComparesTheStampInUTC(t *testing.T) {
	pid := os.Getpid()
	utc := psStart(t, "UTC")
	local := psStart(t, "")
	if utc == "" {
		t.Skip("no ps lstart on this platform")
	}
	if utc == local {
		t.Skip("this machine runs in UTC, so the two forms cannot be distinguished")
	}
	if !Alive(pid, utc) {
		t.Errorf("a live process with its own UTC start stamp (%q) was reported dead. "+
			"Every session on the machine would be skipped and no wake would ever "+
			"be delivered", utc)
	}
	if Alive(pid, local) {
		t.Errorf("a LOCAL start stamp (%q) was accepted where the harness writes UTC "+
			"(%q). Accepting either means the guard is not comparing anything, and "+
			"a recycled pid would pass it", local, utc)
	}
}

func psStart(t *testing.T, tz string) string {
	t.Helper()
	cmd := exec.Command("ps", "-o", "lstart=", "-p", strconv.Itoa(os.Getpid()))
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	if tz != "" {
		cmd.Env = append(cmd.Env, "TZ="+tz)
	}
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// A key with no stamp at all falls back to liveness, which is weaker and is the
// most that can honestly be claimed: better than refusing to wake anybody, and
// it must not silently read as a verified match.
func TestAKeyWithNoStampFallsBackToLiveness(t *testing.T) {
	if !Alive(os.Getpid(), "") {
		t.Error("this process reported itself dead with no stamp to compare")
	}
	if Alive(-1, "") {
		t.Error("an impossible pid reported alive")
	}
}

// A receiver that rejects everything is indistinguishable from one that
// accepts, and that limit is recorded here rather than left to be rediscovered.
//
// Deliver returning nil proves the kernel took the bytes and nothing else.
// There is no acknowledgement in this protocol: measured against a live Claude
// Code session, a correct peer token and a deliberately wrong one both produced
// two successful writes and a zero-byte EOF. So no caller may report a
// successful write as "the agent was woken".
//
// This test exists so that a future change claiming confirmation has to delete
// it deliberately. If the harness ever gains an acknowledgement, this is the
// test that should start failing.
func TestDeliverCannotTellAcceptanceFromRejection(t *testing.T) {
	const pid, session, token = 4250, "s", "768509232a525210ee1a5a6d9699de93"
	home, sock := fixture(t, pid, session, token, "", true)

	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	// A receiver that reads the bytes and refuses them, saying nothing.
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_, _ = io.Copy(io.Discard, c)
			_ = c.Close() // rejected, silently, exactly as the real one does
		}
	}()

	sessions, err := Discover(home, alwaysAlive)
	if err != nil || len(sessions) != 1 {
		t.Fatalf("setup: discovered %d session(s): %v", len(sessions), err)
	}
	if err := Deliver(context.Background(), sessions[0], "Dibs: check the board."); err != nil {
		t.Fatalf("delivery to a rejecting receiver reported an error (%v). If the "+
			"protocol has gained an acknowledgement, this limitation is over: say "+
			"so in Deliver's comment and let callers claim confirmation", err)
	}
	t.Log("recorded: a rejecting receiver is indistinguishable from an accepting " +
		"one, so a nil return means written, never delivered")
}

// Package peerwake delivers a wake over a harness's own cross-session socket.
//
// THE PROBLEM THIS SOLVES. Dibs' wake path resolved an agent by the session id
// its lifecycle hooks quote, and that id has to match the one register bound.
// Getting those to agree took a night of measurement on this project's own
// board and produced four wrong diagnoses on the way: the ids come from
// different namespaces, register can bind one the hooks never send, a swept row
// can leave a live session's id to be inherited by the next agent in its
// directory. Every one of those is a symptom of the same thing: Dibs was
// inferring which process to reach.
//
// It does not have to infer. Claude Code publishes, per session, a unix socket
// and an authentication key, both discoverable on disk, and it accepts a
// newline-delimited JSON message on that socket. So the address is READ rather
// than guessed, and the failure mode changes from "the wake reached nobody and
// nothing said so" to "the socket was not there", which is observable.
//
// WHAT THIS IS NOT. It is not steering. The only thing sent is Dibs' own
// notice that mail is waiting, exactly what [wake.exec] delivers today: no
// peer-authored text, no instruction, nothing about what the agent should do
// next. WAKE-MECHANISMS.md rejected Codex's `turn/steer` because owning a
// thread over a socket is orchestration; this is the opposite end of that
// spectrum, and the difference is not merely one of degree. Delivery here is
// mediated by the recipient's own harness, which labels the message as coming
// from a peer, tells the model it is not user input, and gates it on the
// recipient's permission mode: a message sent as a PEER may be held for that
// human's approval before it is shown at all. Dibs cannot bypass any of that
// and does not try to.
//
// TRUST. The socket is 0600 in a 0700 directory the harness refuses to use if
// it is shared or foreign-owned, and the key is 0600 beside the session
// sidecar. Reaching either already requires being the same user, which is the
// same boundary ~/.dibs/local.secret sits behind, so this grants no reach that
// a local process did not already have.
package peerwake

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Session is one harness session this machine can deliver to.
type Session struct {
	PID       int
	SessionID string // the id the harness's own hooks quote
	Socket    string
	token     string // never logged, never returned in a result
}

// keyFile is <pid>.<64 hex>.key, which is the shape the harness writes and the
// shape it refuses to read anything else as. Anchored, because this names a
// path component: a pid that is not digits is not a pid.
var keyFile = regexp.MustCompile(`^(\d+)\.[0-9a-f]{64}\.key$`)

// peerToken is 32 hex characters. Checked rather than assumed, so a truncated
// or half-written key file is refused here instead of failing as an auth error
// against a live session.
var peerToken = regexp.MustCompile(`^[0-9a-f]{32}$`)

// maxSocketPath is the harness's own bound, and it is a platform limit rather
// than a policy: a unix socket path longer than this cannot be bound at all, so
// a session whose path would exceed it lives somewhere else entirely and is not
// ours to guess at.
const maxSocketPath = 103

// Discover lists the sessions on this machine that can be woken, newest first.
//
// Reads only: the sidecar directory the harness writes for itself. Nothing here
// starts a process, and a session that is not running is skipped rather than
// reported, because a stale key file is exactly what a crashed session leaves
// behind and waking one is how a wake path comes to lie about delivery.
func Discover(home string, alive func(pid int, procStart string) bool) ([]Session, error) {
	dir := filepath.Join(home, ".claude", "sessions")
	ents, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil // no Claude Code on this machine: not an error
		}
		return nil, fmt.Errorf("reading %s: %w", dir, err)
	}
	var out []Session
	for _, e := range ents {
		m := keyFile.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		pid, err := strconv.Atoi(m[1])
		if err != nil || pid <= 0 {
			continue
		}
		s, ok := load(dir, pid, filepath.Join(dir, e.Name()), alive)
		if ok {
			out = append(out, s)
		}
	}
	return out, nil
}

// load reads one session's key and sidecar, and returns it only if everything
// agrees and the process is still the one the key was written for.
func load(dir string, pid int, key string, alive func(int, string) bool) (Session, bool) {
	var k struct {
		PeerToken string `json:"peerToken"`
		ProcStart string `json:"procStart"`
	}
	b, err := os.ReadFile(key) // #nosec G304 -- name matched keyFile; pid is digits
	if err != nil || json.Unmarshal(b, &k) != nil {
		return Session{}, false
	}
	if !peerToken.MatchString(k.PeerToken) {
		return Session{}, false
	}
	// THE PID-REUSE GUARD, and it is the harness's own. A pid is reused within
	// hours on a busy machine, and the socket for a dead session is removed
	// while its key file may not be, so "the pid is alive" is not enough: it has
	// to be alive AND have started when the key says. Skipping this would let a
	// wake go to whatever process inherited the number.
	if alive != nil && !alive(pid, k.ProcStart) {
		return Session{}, false
	}
	var side struct {
		SessionID string `json:"sessionId"`
	}
	sb, err := os.ReadFile(filepath.Join(dir, strconv.Itoa(pid)+".json")) // #nosec G304
	if err != nil || json.Unmarshal(sb, &side) != nil || side.SessionID == "" {
		return Session{}, false
	}
	sock, ok := socketFor(pid)
	if !ok {
		return Session{}, false
	}
	if fi, err := os.Stat(sock); err != nil || fi.Mode()&os.ModeSocket == 0 {
		return Session{}, false // no live endpoint: nothing to deliver to
	}
	return Session{PID: pid, SessionID: side.SessionID, Socket: sock, token: k.PeerToken}, true
}

// socketFor is where the harness binds, and every part of this order was
// measured rather than assumed.
//
// NOT os.TempDir(). That reads $TMPDIR, which on macOS is a long per-user path
// under /var/folders, and the harness does not bind there: the sockets are in
// /tmp/cc-socks. An earlier version of this function used os.TempDir and
// discovered ZERO of the six live sessions on the machine it was written on,
// while every unit test passed, because the tests set XDG_RUNTIME_DIR and so
// never exercised the default. A live probe caught it. The literal is the
// correct answer here and the portable-looking call is the wrong one.
//
// CLAUDE_CODE_TMPDIR is the harness's own override, named in its own error
// message about socket paths that are too long, so it is honoured before the
// default for the same reason XDG_RUNTIME_DIR is.
func socketFor(pid int) (string, bool) {
	base := os.Getenv("XDG_RUNTIME_DIR")
	if base == "" {
		base = os.Getenv("CLAUDE_CODE_TMPDIR")
	}
	if base == "" {
		base = "/tmp"
	}
	p := filepath.Join(base, "cc-socks", strconv.Itoa(pid)+".sock")
	if len(p) > maxSocketPath {
		return "", false
	}
	return p, true
}

// Alive reports whether this pid is still the process the key was written for.
//
// TZ=UTC, AND THAT IS THE WHOLE FUNCTION. The harness records procStart by
// running `LC_ALL=C TZ=UTC ps -o lstart=`, so the stamp in the key file is UTC.
// A plain `ps -o lstart=` answers in LOCAL time, and the two differ by the
// machine's offset while agreeing to the second, which reads exactly like a
// recycled pid. Comparing them naively rejected all six live sessions on the
// machine this was written on: every wake would have been skipped, silently,
// with the guard doing its job and reporting nothing, because "no session was
// wakeable" and "the clock is off by seven hours" look identical from here.
//
// LC_ALL=C for the same reason: the month name is locale-dependent, so a
// machine in another locale would fail this comparison for a third reason that
// also looks like a recycled pid.
//
// An empty procStart means the key predates the stamp, or the platform records
// it differently: liveness alone is the answer then, which is weaker and is the
// best that can honestly be said.
func Alive(pid int, procStart string) bool {
	cmd := exec.Command("ps", "-o", "lstart=", "-p", strconv.Itoa(pid)) // #nosec G204 -- pid is an int
	cmd.Env = append(os.Environ(), "LC_ALL=C", "TZ=UTC")
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	got := strings.TrimSpace(string(out))
	if got == "" {
		return false
	}
	if strings.TrimSpace(procStart) == "" {
		return true
	}
	return got == strings.TrimSpace(procStart)
}

// Deliver hands one notice to one session and returns when it has been written.
//
// COUNTS AND SENDERS, NEVER BODIES. The caller passes the same digest the rest
// of the wake path uses, and that rule lives there: this function will send
// whatever it is given, so it is the caller's business not to give it a message
// body. Said here because a transport is exactly where such a rule stops being
// enforced by anything.
func Deliver(ctx context.Context, s Session, notice string) error {
	if strings.TrimSpace(notice) == "" {
		return errors.New("refusing to deliver an empty notice")
	}
	d := net.Dialer{Timeout: 2 * time.Second}
	conn, err := d.DialContext(ctx, "unix", s.Socket)
	if err != nil {
		return fmt.Errorf("dial %s: %w", s.Socket, err)
	}
	defer func() { _ = conn.Close() }()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	} else {
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	}
	// Two lines, in this order. The auth line first because the harness refuses
	// the connection outright on an unauthenticated one when it requires auth,
	// and treats a blank or unparseable first line as that refusal.
	if err := writeLine(conn, map[string]any{"type": "auth", "token": s.token}); err != nil {
		return fmt.Errorf("auth: %w", err)
	}
	if err := writeLine(conn, map[string]any{
		"type":    "user",
		"message": map[string]any{"role": "user", "content": notice},
	}); err != nil {
		return fmt.Errorf("notice: %w", err)
	}
	// Half-close so the harness sees the end of the message rather than waiting
	// on a connection we are finished with. It sets allowHalfOpen for this.
	if tc, ok := conn.(*net.UnixConn); ok {
		_ = tc.CloseWrite()
	}
	return nil
}

func writeLine(w net.Conn, v map[string]any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

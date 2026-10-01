package main

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// awaitEnv points `dibs await` at addr with a local secret it can read.
func awaitEnv(t *testing.T, addr string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "local.secret"),
		[]byte(strings.Repeat("a", 64)), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DIBS_DIR", dir)
	t.Setenv("DIBS_ADDR", "http://"+addr)
	t.Setenv("DIBS_TOKEN", "agent-token")
	restore := awaitReconnectFor
	t.Cleanup(func() { awaitReconnectFor = restore })
	// Each test resolves the address afresh. The cache is per process, so
	// without this a second run in the same process dialled the FIRST run's
	// closed server, which is how the cache's behaviour on a moved daemon was
	// noticed at all.
	reresolveMCPEndpoint()
	t.Cleanup(reresolveMCPEndpoint)
}

// awaitReply answers a tools/call the way the daemon does.
func awaitReply(w http.ResponseWriter, payload map[string]any) {
	text, _ := json.Marshal(payload)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": "2.0", "id": 1,
		"result": map[string]any{
			"isError": false,
			"content": []map[string]any{{"type": "text", "text": string(text)}},
		},
	})
}

// A daemon restart is routine, and `dibs await` rides through it.
//
// k7-dev ran `dibs await -since 469 -timeout 8h` in the background and lost
// it to a daemon restart: the connection dropped with an EOF and the watcher
// ended. Restarts are ordinary (upgrades, config reloads; several on the day
// it was reported), and -since makes resuming EXACT, because the cursor names
// precisely what the caller has already seen. So a watcher that gives up on
// the first dropped connection is a watcher an agent has to babysit, which is
// the job it exists to remove.
//
// Safe to retry on ANY transport error, including a reset, which the bridge
// deliberately does not do for writes (dialFailed): await_events is a read, so
// asking twice costs nothing. Same ambiguity, opposite resolution, for the same
// reason the surrender path gives: the stake differs.
func TestAwaitRidesThroughADaemonRestart(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The first two requests die mid-flight, the way a daemon going down
		// for a restart drops them: the connection closes with no response.
		if calls.Add(1) <= 2 {
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Error("setup: cannot hijack to simulate a dropped connection")
				return
			}
			c, _, _ := hj.Hijack()
			_ = c.Close()
			return
		}
		awaitReply(w, map[string]any{
			"events": []any{map[string]any{"type": "message.sent", "serial": 7}},
			"serial": 7,
		})
	}))
	defer srv.Close()
	awaitEnv(t, srv.Listener.Addr().String())

	err := await([]string{"-since", "5", "-timeout", "10s"})
	if err != nil {
		t.Fatalf("await gave up on a daemon that came back: %v. A restart is routine "+
			"and -since makes resuming exact, so the watcher should have ridden it", err)
	}
	if n := calls.Load(); n < 3 {
		t.Errorf("setup: the server saw %d request(s), so the dropped connections never "+
			"happened and this proves nothing about riding through them", n)
	}
}

// A daemon that stays gone is reported with its OWN exit code.
//
// Timeout and "the daemon is unreachable" both exited 1, so a harness waking on
// the watcher's exit could not tell "nothing happened for eight hours" from
// "nothing can happen, the board is down". 75 is EX_TEMPFAIL in sysexits: a
// temporary failure, try again later, which is exactly the situation.
func TestAwaitSaysTheDaemonIsGoneWithItsOwnExitCode(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	where := probe.Addr().String()
	_ = probe.Close() // nothing listens there now
	awaitEnv(t, where)
	awaitReconnectFor = 300 * time.Millisecond

	err = await([]string{"-since", "5", "-timeout", "10s"})
	var coded interface{ exitStatus() int }
	if !errors.As(err, &coded) || coded.exitStatus() != exitTempFail {
		t.Fatalf("an unreachable daemon returned %v; want exit status %d, distinct from a "+
			"timeout, so a harness can tell the board is down from the board is quiet",
			err, exitTempFail)
	}
	if !strings.Contains(err.Error(), "-since 5") {
		t.Errorf("the error does not say how to resume: %v. The cursor is the whole "+
			"reason resuming is safe, so it should be in the message", err)
	}
}

// And a timeout is still exit 1, unchanged, so nothing that relies on it breaks.
func TestAwaitTimeoutIsStillExitOne(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		awaitReply(w, map[string]any{"events": []any{}, "serial": 5})
	}))
	defer srv.Close()
	awaitEnv(t, srv.Listener.Addr().String())

	err := await([]string{"-since", "5", "-timeout", "200ms"})
	if err == nil {
		t.Fatal("a timeout with no events returned success")
	}
	var coded interface{ exitStatus() int }
	if errors.As(err, &coded) {
		t.Errorf("a plain timeout carried exit status %d: it must stay 1, which existing "+
			"callers already treat as 'nothing arrived'", coded.exitStatus())
	}
}

// A daemon that comes back on a different address is followed, not redialled.
//
// `dibs await` resolves the daemon's address once per process, so that a
// command polling for eight hours does not spawn Supgang on every poll. Without
// re-resolving on failure, riding through a restart would only ever work when
// the daemon came back in the same place, and a hub that moved (the case the
// resolver exists for) would be redialled at its old address until the watcher
// gave up. Re-resolving only when a call got no answer costs nothing on the
// happy path, which is the rule AGENTS.md gives any long lived process.
func TestAwaitFollowsADaemonThatCameBackElsewhere(t *testing.T) {
	moved := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		awaitReply(w, map[string]any{
			"events": []any{map[string]any{"type": "message.sent", "serial": 9}},
			"serial": 9,
		})
	}))
	defer moved.Close()
	var wentAway atomic.Bool
	gone := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The daemon goes down mid-call, and is next found somewhere else.
		_ = os.Setenv("DIBS_ADDR", "http://"+moved.Listener.Addr().String())
		wentAway.Store(true)
		if hj, ok := w.(http.Hijacker); ok {
			c, _, _ := hj.Hijack()
			_ = c.Close()
		}
	}))
	defer gone.Close()
	awaitEnv(t, gone.Listener.Addr().String())

	if err := await([]string{"-since", "5", "-timeout", "10s"}); err != nil {
		t.Fatalf("await did not follow the daemon to its new address: %v", err)
	}
	if !wentAway.Load() {
		t.Error("setup: the first daemon was never called, so nothing moved")
	}
}

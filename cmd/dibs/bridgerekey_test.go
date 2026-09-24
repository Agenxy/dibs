package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// The bridge holds no machine state between requests.
//
// It used to read `local.secret` once, at spawn, and send that string for the
// life of the process. Resetting a board mints a new secret, so every session
// that was already running got 401 from every Dibs call for the rest of its
// life: nine of them on this machine on 2026-09-22, each with a working
// bridge process, a reachable daemon and a credential from a board that no
// longer existed. Nothing said so. The agent saw an authorisation error and
// had no way to act on it, because the corrective action was "restart your
// harness", which is not a thing an agent can do.
//
// The surrounding code already promised better. `doWithRestartGrace` says a
// session "reattaches by itself the moment the daemon is back", and
// `boardOrigin` says it resolves the hub's address NOW "rather than the one it
// had when the config was printed". Both were true only while nothing about
// the board changed, which is not the case the promises were written for.
//
// This is also what MCP 2026 asks for. A stateless protocol means a long lived
// bridge process has to behave as though it were spawned fresh for each
// request, and a credential read once at startup is precisely the kind of
// session state 2026 removes. The per-call plugins (pi, opencode) get this for
// free by spawning a bridge per call; this one has to earn it.
//
// Drives the REAL loop over real pipes, because the claim is about what the
// process does across a rotation, not about what a helper returns.
func TestTheBridgeFollowsTheSecretWhenTheBoardIsRekeyed(t *testing.T) {
	dir := t.TempDir()
	secretPath := filepath.Join(dir, "local.secret")
	first := strings.Repeat("a", 64)
	second := strings.Repeat("b", 64)
	if err := os.WriteFile(secretPath, []byte(first), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DIBS_DIR", dir)

	// A daemon that accepts exactly one secret at a time, as the real one
	// does: it compares against the file it is serving from.
	var want atomic.Value
	want.Store(first)
	var seen atomic.Int64
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{
		ReadHeaderTimeout: 5 * time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			seen.Add(1)
			if r.Header.Get("X-Dibs-Local") != want.Load().(string) {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte("unauthorized"))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"ok":true}}` + "\n"))
		}),
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	t.Setenv("DIBS_ADDR", "http://"+ln.Addr().String())

	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, stdout := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = inR, outW
	t.Cleanup(func() { os.Stdin, os.Stdout = stdin, stdout })

	exited := make(chan error, 1)
	go func() { exited <- runBridge(nil) }()
	t.Cleanup(func() {
		_ = inW.Close()
		select {
		case <-exited:
		case <-time.After(5 * time.Second):
			t.Error("the bridge did not exit when stdin closed")
		}
	})

	replies := bufio.NewReader(outR)
	call := func(id int) map[string]any {
		t.Helper()
		if _, werr := fmt.Fprintf(inW,
			`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":"board"}}`+"\n",
			id); werr != nil {
			t.Fatal(werr)
		}
		done := make(chan []byte, 1)
		go func() {
			line, rerr := replies.ReadBytes('\n')
			if rerr != nil {
				close(done)
				return
			}
			done <- line
		}()
		select {
		case e := <-exited:
			t.Fatalf("the bridge exited instead of answering: %v", e)
			return nil
		case line, ok := <-done:
			if !ok {
				t.Fatal("stdout closed: the bridge is gone")
			}
			var m map[string]any
			if uerr := json.Unmarshal(line, &m); uerr != nil {
				t.Fatalf("not JSON-RPC on stdout: %v (%q)", uerr, line)
			}
			return m
		case <-time.After(10 * time.Second):
			t.Fatal("no reply and no exit")
			return nil
		}
	}

	if got := call(1); got["error"] != nil {
		t.Fatalf("the first call, with the secret the bridge started on, failed: %v", got)
	}

	// The board is reset: same address, same bridge, new credential on disk.
	want.Store(second)
	if err := os.WriteFile(secretPath, []byte(second+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	after := call(2)
	if after["error"] != nil {
		t.Fatalf("after a rekey the bridge still sent the old secret: %v\n\n"+
			"A bridge that caches the credential turns a board reset into a dead "+
			"session for every agent already running, with no corrective action an "+
			"agent can take. Read it per request.", after)
	}
	if seen.Load() < 2 {
		t.Fatalf("the daemon saw %d requests; the test proved nothing", seen.Load())
	}
}

// And it follows the board when the board moves.
//
// The endpoint was resolved once, at spawn, exactly as the credential was.
// `doWithRestartGrace` then retried a failed call against that same URL for
// ten seconds and gave up, so a daemon that came back at a DIFFERENT address
// was unreachable for the life of every session already running, even though
// the address it moved to was written down in the config the bridge reads.
//
// That is the same defect as the stale credential above and it deserves the
// same answer, but not the same mechanism: re-reading the address on every
// request costs a config parse on the happy path and, when the hub is named
// as a Supgang peer, a process. A stale address only ever manifests as a
// request that does not arrive, so the resolution belongs exactly there: free
// while it is working, and self healing the moment it is not. This is also
// what makes doWithRestartGrace's own promise true, that a session
// "reattaches by itself the moment the daemon is back".
func TestTheBridgeFollowsTheBoardWhenItComesBackSomewhereElse(t *testing.T) {
	restore := upgradeGrace
	upgradeGrace = 4 * time.Second
	t.Cleanup(func() { upgradeGrace = restore })

	dir := t.TempDir()
	secret := strings.Repeat("c", 64)
	if err := os.WriteFile(filepath.Join(dir, "local.secret"), []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DIBS_DIR", dir)
	// The address comes from the CONFIG, not the environment, because that is
	// where an operator moving a board writes it and because the environment
	// cannot be changed under a running bridge without racing it.
	t.Setenv("DIBS_ADDR", "")
	writeAddr := func(where string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "dibs.toml"),
			[]byte("addr = \""+where+"\"\ninsecure_plaintext = true\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	serve := func() (*http.Server, string) {
		t.Helper()
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		srv := &http.Server{
			ReadHeaderTimeout: 5 * time.Second,
			Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"ok":true}}` + "\n"))
			}),
		}
		// NO KEEP-ALIVES, AND THIS IS THE TEST'S WHOLE RELIABILITY.
		//
		// This failed on CI and passed 25 times in a row locally, which is the
		// shape this repository has learned not to write off. The cause is not
		// timing noise, it is that the two machines produced DIFFERENT ERRORS
		// for the same event. With pooling on, the bridge keeps an idle
		// connection to the old board; closing that board makes the next send
		// fail with ECONNRESET. Locally the pooled connection had already gone,
		// so the send dialled fresh and got ECONNREFUSED.
		//
		// Only one of those is retried, deliberately: dialFailed() is refused
		// and nothing else, because a reset cannot prove the request was not
		// already applied and a retried claim is worse than a failed call. So
		// with pooling on this test was asserting something the product
		// promises not to do, and it passed only when the pool happened to be
		// empty.
		//
		// Turning keep-alives off makes every send a fresh dial, which is the
		// case re-resolution is actually for: the board has gone and a new one
		// is somewhere else. What happens to an in-flight pooled connection is
		// a different question with a different answer (op_id), and a test that
		// covers it should say so in its name.
		srv.SetKeepAlivesEnabled(false)
		go func() { _ = srv.Serve(ln) }()
		return srv, ln.Addr().String()
	}

	first, whereFirst := serve()
	writeAddr(whereFirst)

	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, stdout := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = inR, outW
	t.Cleanup(func() { os.Stdin, os.Stdout = stdin, stdout })

	exited := make(chan error, 1)
	go func() { exited <- runBridge(nil) }()
	t.Cleanup(func() {
		_ = inW.Close()
		select {
		case <-exited:
		case <-time.After(5 * time.Second):
			t.Error("the bridge did not exit when stdin closed")
		}
	})

	replies := bufio.NewReader(outR)
	call := func(id int) map[string]any {
		t.Helper()
		if _, werr := fmt.Fprintf(inW,
			`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":"board"}}`+"\n",
			id); werr != nil {
			t.Fatal(werr)
		}
		done := make(chan []byte, 1)
		go func() {
			line, rerr := replies.ReadBytes('\n')
			if rerr != nil {
				close(done)
				return
			}
			done <- line
		}()
		select {
		case e := <-exited:
			t.Fatalf("the bridge exited instead of answering: %v", e)
			return nil
		case line, ok := <-done:
			if !ok {
				t.Fatal("stdout closed: the bridge is gone")
			}
			var m map[string]any
			if uerr := json.Unmarshal(line, &m); uerr != nil {
				t.Fatalf("not JSON-RPC on stdout: %v (%q)", uerr, line)
			}
			return m
		case <-time.After(15 * time.Second):
			t.Fatal("no reply and no exit")
			return nil
		}
	}

	if got := call(1); got["error"] != nil {
		t.Fatalf("the first call, to the address the bridge started on, failed: %v", got)
	}

	// The board moves: the old one is gone and the config names the new one.
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, whereSecond := serve()
	t.Cleanup(func() { _ = second.Close() })
	writeAddr(whereSecond)

	after := call(2)
	if after["error"] != nil {
		t.Fatalf("the bridge kept dialling %s after the board moved to %s: %v\n\n"+
			"The address is written down where the bridge can read it, and the only "+
			"symptom of a stale one is the request that did not arrive. Re-resolve "+
			"there.", whereFirst, whereSecond, after)
	}
}

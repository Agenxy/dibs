// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/harnessenv"
	"github.com/agenxy/dibs/internal/ledger"
	"github.com/agenxy/dibs/internal/mcp"
	"github.com/agenxy/dibs/internal/paths"
	"github.com/agenxy/dibs/internal/testport"
)

// A successful next tool call cannot repair a harness that discarded the
// connection when initialize failed. Enter through the real stdio loop, keep
// the board absent beyond the ordinary ten-second retry, and then serve the
// real MCP implementation. Discovery must preserve the daemon's own contract.
func TestBridgeStartupWaitsForALateDaemon(t *testing.T) {
	for _, method := range []string{"server/discover", "initialize", "tools/list"} {
		t.Run(method, func(t *testing.T) {
			t.Parallel()
			lateBridgeStartup(t, method)
		})
	}
}

func TestBridgeStartupHelper(t *testing.T) {
	if os.Getenv("DIBS_TEST_STARTUP_BRIDGE") != "1" {
		return
	}
	if marker := os.Getenv("DIBS_TEST_RECONNECT_SLOW_STAMP"); marker != "" {
		original := lookupSelfStart
		lookupSelfStart = func() string {
			if err := os.WriteFile(marker, []byte("entered"), 0o600); err != nil {
				t.Error("slow start-stamp setup:", err)
				return ""
			}
			time.Sleep(1500 * time.Millisecond)
			return original()
		}
	}
	if os.Getenv("DIBS_TEST_RECONNECT_OPEN_LOG") != "" {
		installReconnectFakeAppOpen()
	}
	if gate := os.Getenv("DIBS_TEST_SURFACE_GATE"); gate != "" {
		if os.Getenv("DIBS_TEST_SURFACE_FAIL_ONCE") == "1" {
			go observeSurfaceCompletionWaiter(t, gate)
		}
		original := detectedSurface
		failed := false
		detectedSurface = func(ctx context.Context) (string, error) {
			f, err := os.OpenFile(gate, os.O_RDWR, 0o600)
			if err != nil {
				t.Error("surface gate setup:", err)
				return "", err
			}
			defer func() { _ = f.Close() }()
			if err := os.WriteFile(gate+".entered", []byte("entered"), 0o600); err != nil {
				t.Error("surface gate marker:", err)
				return "", err
			}
			if err := paths.LockExclusive(f, true); err != nil {
				t.Error("surface gate lock:", err)
				return "", err
			}
			if os.Getenv("DIBS_TEST_SURFACE_FAIL_ONCE") == "1" && !failed {
				failed = true
				if err := os.WriteFile(gate+".failed", []byte("failed"), 0o600); err != nil {
					t.Error("surface failure marker:", err)
				}
				return "", context.DeadlineExceeded
			}
			return original(ctx)
		}
	}
	if gate := os.Getenv("DIBS_TEST_START_STAMP_GATE"); gate != "" {
		failed := false
		lookupSelfStart = func() string {
			f, err := os.OpenFile(gate, os.O_RDWR, 0o600)
			if err != nil {
				t.Error("stamp gate setup:", err)
				return ""
			}
			defer func() { _ = f.Close() }()
			if err := paths.LockExclusive(f, true); err != nil {
				t.Error("stamp gate lock:", err)
				return ""
			}
			if os.Getenv("DIBS_TEST_START_STAMP_FAIL_ONCE") == "1" && !failed {
				failed = true
				if err := os.WriteFile(gate+".failed", []byte("failed"), 0o600); err != nil {
					t.Error("stamp failure marker:", err)
				}
				return ""
			}
			return harnessenv.ProcessStart(os.Getpid())
		}
	}
	if err := runBridge(nil); err != nil {
		t.Fatal(err)
	}
}

// Observe the real worker blocked on the held lookup before releasing it to
// fail. A registration reply alone does not establish that: the worker might
// not run until after failure and start a fresh probe, masking the regression.
// This observer runs only in the private child; it changes no production state.
func observeSurfaceCompletionWaiter(t *testing.T, gate string) {
	t.Helper()
	stack := make([]byte, 256<<10)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		n := runtime.Stack(stack, true)
		for _, goroutine := range bytes.Split(stack[:n], []byte("\n\n")) {
			if bytes.Contains(goroutine, []byte("[select]:\n")) &&
				bytes.Contains(goroutine, []byte("(*surfaceCompletion).advance")) {
				if err := os.WriteFile(gate+".waiting", []byte("waiting"), 0o600); err != nil {
					t.Error("surface waiter marker:", err)
				}
				return
			}
		}
		time.Sleep(time.Millisecond)
	}
}

// A reconnect fixture runs the real bridge, but its app contact is a
// per-process fake. TestMain's generic guard is installed too late for a
// package-init copy of RealShower, so the production shower must retain a
// reference to this injected value rather than a copy.
func installReconnectFakeAppOpen() {
	logPath := os.Getenv("DIBS_TEST_RECONNECT_OPEN_LOG")
	harnessenv.RealShower = harnessenv.Shower{
		Ownership: harnessenv.ChatGPTOwnership,
		Open: func(argv []string) error {
			if len(argv) != 3 || argv[0] != "/usr/bin/open" || argv[1] != "-g" ||
				!strings.HasPrefix(argv[2], "codex://threads/") {
				return fmt.Errorf("unexpected reconnect app open: %q", argv)
			}
			f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(f, argv[2])
			closeErr := f.Close()
			if err != nil {
				return err
			}
			return closeErr
		},
	}
}

// A bound on repeated dials alone misses a server that accepts the request
// but never sends headers. The whole startup must still answer before the
// harness's thirty-second deadline, including the read of the response.
func TestBridgeStartupWaitIsBoundedWhenTheDaemonAcceptsButNeverAnswers(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		<-release
	}))
	defer func() { close(release); srv.Close() }()
	line := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	req, err := http.NewRequest(http.MethodPost, srv.URL, bytes.NewReader(line))
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	out := &syncWriter{w: bufio.NewWriter(&buf)}
	start := time.Now()
	forward(srv.Client(), req, line, out, nil)
	out.flush()
	if elapsed := time.Since(start); elapsed > 27*time.Second {
		t.Fatalf("startup outlived its bound: %s", elapsed)
	}
	var reply map[string]any
	if err := json.Unmarshal(buf.Bytes(), &reply); err != nil || reply["error"] == nil {
		t.Fatalf("unavailable startup did not answer with an error: %s (%v)", buf.String(), err)
	}
}

func lateBridgeStartup(t *testing.T, method string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "local.secret"), []byte(strings.Repeat("a", 64)), 0o600); err != nil {
		t.Fatal(err)
	}
	// Keep the real connection-refused outage without leaving an ephemeral
	// address for a sibling :0 server to acquire during the twenty seconds.
	addr := testport.Outage(t)
	testport.RequireOutageAddress(t, addr)
	cmd := exec.Command(os.Args[0], "-test.run=^TestBridgeStartupHelper$") // own test binary
	cmd.Env = append(os.Environ(), "DIBS_TEST_STARTUP_BRIDGE=1", "DIBS_DIR="+dir, "DIBS_ADDR=http://"+addr)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = in.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() })
	params := map[string]any{"protocolVersion": "2025-11-25", "clientInfo": map[string]any{"name": "startup-fixture", "version": "1"}}
	if method == "server/discover" {
		params = map[string]any{"_meta": map[string]any{"protocolVersion": "2026-07-28"}}
	}
	start := time.Now()
	if err := json.NewEncoder(in).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params}); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(out)
	replies := make(chan []byte, 1)
	go func() { line, _ := reader.ReadBytes('\n'); replies <- line }()
	// This is the actual twenty-second outage in the report, not a shortened
	// test-only grace that could conceal the production startup window.
	select {
	case line := <-replies:
		t.Fatalf("%s failed before the daemon arrived: %s", method, line)
	case <-time.After(20 * time.Second):
	}
	srv := lateStartupServer(t, dir, addr)
	defer func() { _ = srv.Close() }()
	assertStartupReply(t, replies, 1)
	t.Logf("%s recovered after %s", method, time.Since(start))
	if _, err := fmt.Fprintln(in, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`); err != nil {
		t.Fatal(err)
	}
	go func() { line, _ := reader.ReadBytes('\n'); replies <- line }()
	result := assertStartupReply(t, replies, 2)
	tools, _ := result["tools"].([]any)
	if len(tools) == 0 {
		t.Fatal("startup succeeded but the real daemon's tools were not discovered")
	}
}

func lateStartupServer(t *testing.T, dir, addr string) *http.Server {
	t.Helper()
	eng := startupEngine(t, dir)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	return serveStartupDaemon(eng, ln)
}

// startupEngine runs a real engine over a ledger in dir until the test ends.
func startupEngine(t *testing.T, dir string) *engine.Engine {
	t.Helper()
	box, err := ledger.LoadOrCreateKey(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	led, err := ledger.Open(filepath.Join(dir, "ledger.jsonl"), "startup", box)
	if err != nil {
		t.Fatal(err)
	}
	eng := engine.New(core.NewState("startup", core.DefaultLimits()), led, nil)
	ctx, cancel := context.WithCancel(context.Background())
	go eng.Run(ctx)
	t.Cleanup(func() { cancel(); _ = led.Close() })
	return eng
}

// serveStartupDaemon serves the real MCP implementation on a listener the
// caller already holds.
func serveStartupDaemon(eng *engine.Engine, ln net.Listener) *http.Server {
	srv := &http.Server{Handler: mcp.New(eng), ReadHeaderTimeout: time.Second}
	go func() { _ = srv.Serve(ln) }()
	return srv
}

// startupDaemon is lateStartupServer for a daemon that is up from the start:
// it binds its own listener, so no address is ever released.
func startupDaemon(t *testing.T, dir string) (*http.Server, string) {
	t.Helper()
	eng := startupEngine(t, dir)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return serveStartupDaemon(eng, ln), ln.Addr().String()
}

func assertStartupReply(t *testing.T, replies <-chan []byte, id int) map[string]any {
	t.Helper()
	select {
	case line := <-replies:
		var reply map[string]any
		if err := json.Unmarshal(line, &reply); err != nil {
			t.Fatalf("invalid startup response: %v %s", err, line)
		}
		if reply["id"] != float64(id) || reply["error"] != nil {
			t.Fatalf("startup request %d failed: %s", id, line)
		}
		result, ok := reply["result"].(map[string]any)
		if !ok {
			t.Fatalf("startup has no result: %s", line)
		}
		return result
	case <-time.After(8 * time.Second):
		t.Fatal("daemon is serving but startup never recovered")
		return nil
	}
}

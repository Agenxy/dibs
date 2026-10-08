// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/pprof"
	"sync"
	"testing"
	"time"
)

const economyStderrBytes = 64 << 10

type economyTail struct {
	mu  sync.Mutex
	buf []byte
}

func (s *economyTail) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := len(p)
	if n >= economyStderrBytes {
		s.buf = append(s.buf[:0], p[n-economyStderrBytes:]...)
	} else {
		if keep := economyStderrBytes - n; len(s.buf) > keep {
			copy(s.buf, s.buf[len(s.buf)-keep:])
			s.buf = s.buf[:keep]
		}
		s.buf = append(s.buf, p...)
	}
	return n, nil
}

func (s *economyTail) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return string(s.buf)
}

// Only this fixture's child publishes the diagnostic endpoint. Production
// runBridge is untouched; collection happens after an unchanged 5s failure.
func serveEconomyDiagnostics(t *testing.T) {
	t.Helper()
	path := os.Getenv("DIBS_TEST_ECONOMY_DIAGNOSTICS")
	if path == "" {
		return
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = pprof.Lookup("goroutine").WriteTo(w, 2)
	}))
	t.Cleanup(srv.Close)
	if err := os.WriteFile(path, []byte(srv.URL), 0o600); err != nil {
		t.Fatal("setup: child diagnostic endpoint:", err)
	}
}

type economyDiagnostics struct {
	cmd     *exec.Cmd
	stderr  economyTail
	exited  chan struct{}
	exitErr error
	path    string
}

func newEconomyDiagnostics(dir string) *economyDiagnostics {
	return &economyDiagnostics{path: filepath.Join(dir, "bridge-diagnostic-address"), exited: make(chan struct{})}
}

func (d *economyDiagnostics) attach(cmd *exec.Cmd) {
	d.cmd = cmd
	cmd.Env = append(cmd.Env, "DIBS_TEST_ECONOMY_DIAGNOSTICS="+d.path)
	cmd.Stderr = io.MultiWriter(os.Stderr, &d.stderr)
}

func (d *economyDiagnostics) started() {
	go func() {
		d.exitErr = d.cmd.Wait()
		close(d.exited) // publishes exitErr and ProcessState, read only after this receipt
	}()
}

func (d *economyDiagnostics) stop() {
	_ = d.cmd.Process.Kill()
	<-d.exited
}

func (d *economyDiagnostics) exitState() string {
	select {
	case <-d.exited:
		return fmt.Sprintf("%v; wait error=%v", d.cmd.ProcessState, d.exitErr)
	default:
		return "running or exit not yet reaped (no exit receipt)"
	}
}

func (d *economyDiagnostics) report(readErr error) string {
	read := "pending: ReadBytes has not returned"
	if readErr != nil {
		read = readErr.Error()
	}
	dump := d.bridgeDump()
	return fmt.Sprintf("stdout read=%s\nchild exit=%s\nstderr tail (max %d bytes):\n%s\nbridge goroutines:\n%s",
		read, d.exitState(), economyStderrBytes, d.stderr.String(), dump)
}

func (d *economyDiagnostics) bridgeDump() string {
	address, err := os.ReadFile(d.path)
	if err != nil {
		return "unavailable: child did not publish diagnostics: " + err.Error()
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, string(address), nil)
	if err != nil {
		return "unavailable: invalid child diagnostic address: " + err.Error()
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "unavailable: child stack request: " + err.Error()
	}
	defer func() { _ = res.Body.Close() }()
	const maxDump = 1 << 20
	b, err := io.ReadAll(io.LimitReader(res.Body, maxDump+1))
	if err != nil {
		return "unavailable: child stack read: " + err.Error()
	}
	if len(b) > maxDump {
		return string(b[:maxDump]) + "\n[bridge goroutine dump truncated at 1 MiB]"
	}
	return string(b)
}

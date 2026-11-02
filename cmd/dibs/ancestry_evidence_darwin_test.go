// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

const (
	ancestryEvidenceRoles = 4 // parent, app-shaped helper, two bridges
	ancestryEvidenceBytes = 4096
	ancestryDumpBytes     = 1 << 20
)

var ancestryStageNames = [...]string{
	"helper entered", "app starting", "app started", "bridges starting", "bridges started",
	"request decoded", "request write starting", "request written", "reply read", "reply written",
	"HTTP handler entered", "HTTP handler returned", "bridge running", "bridge returned",
	"ancestry lookup entered", "ancestry lock acquired", "synthetic lookup failed",
}

type ancestryEvidence struct {
	dir    string
	memory []byte
	stages ancestryStages
	start  time.Time
	stderr *os.File
}

func init() {
	ancestryFixtureStages = openAncestryFixtureStages
}

func ancestryCell(memory []byte, role, field int) *int64 {
	// A page-aligned mapping with eight-byte cells; atomics publish complete
	// stamps across the private fixture processes, including at failure time.
	offset := (role*(int(ancestryStageCount)+1) + field) * 8
	return (*int64)(unsafe.Pointer(&memory[offset]))
}

func mapAncestryEvidence(t *testing.T, dir string, role int) ancestryStages {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(dir, "stages"), os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal("ancestry evidence mapping:", err)
	}
	memory, err := syscall.Mmap(int(f.Fd()), 0, ancestryEvidenceBytes, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("ancestry evidence mapping: mmap=%v close=%v", err, closeErr)
	}
	atomic.StoreInt64(ancestryCell(memory, role, 0), int64(os.Getpid()))
	// Child mappings live until this one-shot helper process exits. Unmapping
	// on the helper function's return could race its still-ending lookup.
	return ancestryStages{record: func(stage ancestryStage) {
		atomic.CompareAndSwapInt64(ancestryCell(memory, role, int(stage)+1), 0, time.Now().UnixNano())
	}}
}

func openAncestryFixtureStages(t *testing.T, role int) ancestryStages {
	t.Helper()
	dir := os.Getenv(ancestryEvidenceEnv)
	if dir == "" {
		return ancestryStages{}
	}
	if role == 0 {
		var err error
		role, err = strconv.Atoi(os.Getenv(ancestryRoleEnv))
		if err != nil || role < 2 || role >= ancestryEvidenceRoles {
			t.Fatal("invalid ancestry evidence bridge role")
		}
	}
	// A real file descriptor avoids adding an exec output-copy goroutine.
	// The runtime's SIGQUIT dump writes descriptor 2, not os.Stderr's pointer.
	f, err := os.OpenFile(ancestryStderrPath(dir, role), os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal("ancestry evidence stderr:", err)
	}
	dupErr := syscall.Dup2(int(f.Fd()), 2)
	closeErr := f.Close()
	if dupErr != nil || closeErr != nil {
		t.Fatalf("ancestry evidence stderr: dup=%v close=%v", dupErr, closeErr)
	}
	stages := mapAncestryEvidence(t, dir, role)
	stages.note(ancestryHelperEntered)
	return stages
}

func ancestryStderrPath(dir string, role int) string {
	return filepath.Join(dir, fmt.Sprintf("stderr-%d", role))
}

func newAncestryEvidence(t *testing.T) *ancestryEvidence {
	t.Helper()
	dir := t.TempDir()
	f, err := os.OpenFile(filepath.Join(dir, "stages"), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.Truncate(ancestryEvidenceBytes); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	memory, err := syscall.Mmap(int(f.Fd()), 0, ancestryEvidenceBytes, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("ancestry evidence: mmap=%v close=%v", err, closeErr)
	}
	t.Cleanup(func() { _ = syscall.Munmap(memory) })
	for role := 1; role < ancestryEvidenceRoles; role++ {
		if err := os.WriteFile(ancestryStderrPath(dir, role), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	stderr, err := os.OpenFile(ancestryStderrPath(dir, 1), os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stderr.Close() })
	t.Setenv(ancestryEvidenceEnv, dir)
	atomic.StoreInt64(ancestryCell(memory, 0, 0), int64(os.Getpid()))
	d := &ancestryEvidence{dir: dir, memory: memory, start: time.Now(), stderr: stderr}
	d.stages.record = func(stage ancestryStage) {
		atomic.CompareAndSwapInt64(ancestryCell(memory, 0, int(stage)+1), 0, time.Now().UnixNano())
	}
	return d
}

func (d *ancestryEvidence) handler(real http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.stages.note(ancestryHandlerEntered)
		defer d.stages.note(ancestryHandlerReturned)
		real.ServeHTTP(w, r)
	})
}

func (d *ancestryEvidence) reportFailure(t *testing.T, app *os.Process) {
	t.Helper()
	if !t.Failed() {
		return
	}
	t.Logf("ancestry failure evidence (first fixture boundary stamps; bridge internals unstamped), elapsed=%s", time.Since(d.start))
	for role := range ancestryEvidenceRoles {
		pid := atomic.LoadInt64(ancestryCell(d.memory, role, 0))
		t.Logf("ancestry role=%d pid=%d", role, pid)
		for stage, name := range ancestryStageNames {
			if stamp := atomic.LoadInt64(ancestryCell(d.memory, role, stage+1)); stamp != 0 {
				t.Logf("  %s at=%s elapsed=%s", name, time.Unix(0, stamp).UTC().Format(time.RFC3339Nano), time.Duration(stamp-d.start.UnixNano()))
			}
		}
	}
	stack := make([]byte, ancestryDumpBytes)
	n := runtime.Stack(stack, true)
	t.Logf("ancestry parent goroutines (bounded at 1 MiB):\n%s", stack[:n])
	// Collection happens only after the assertion has failed, before its
	// existing cleanup kills the private helper tree. SIGQUIT asks the Go
	// runtime for stacks and terminates these already-failed helpers.
	// The app owns a fresh process group inherited by its two bridges. Its
	// PID has not been reaped, so this cannot target a reused child PID. One
	// group signal also reaches both bridges before app-death cleanup can.
	if err := syscall.Kill(-app.Pid, syscall.SIGQUIT); err != nil {
		t.Logf("ancestry helper group stack signal: %v", err)
	}
	// This bounded collection wait cannot rescue or extend the failed 5s
	// assertion. There is no wait, scan or log on the success path.
	time.Sleep(100 * time.Millisecond)
	for role := 1; role < ancestryEvidenceRoles; role++ {
		f, err := os.Open(ancestryStderrPath(d.dir, role))
		if err != nil {
			t.Logf("ancestry role=%d stderr unavailable: %v", role, err)
			continue
		}
		b, readErr := io.ReadAll(io.LimitReader(f, ancestryDumpBytes+1))
		closeErr := f.Close()
		if len(b) > ancestryDumpBytes {
			b = b[:ancestryDumpBytes]
		}
		t.Logf("ancestry role=%d goroutines/stderr (bounded at 1 MiB; read=%v close=%v):\n%s", role, readErr, closeErr, b)
	}
}

// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/paths"
)

// fakeBoard registers another live daemon the way a real one does: an entry
// under its pid, held open with the registry lock for its lifetime.
func fakeBoard(t *testing.T, pid int, dir string, parallel bool) {
	t.Helper()
	if err := os.MkdirAll(paths.RunRegistryDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(paths.RunRegistryDir(), strings.TrimSpace(itoa(pid))+".json"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	if err := paths.LockExclusive(f, false); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(paths.Daemon{PID: pid, Addr: "127.0.0.1:4799", Dir: dir, Parallel: parallel})
	if _, err := f.Write(body); err != nil {
		t.Fatal(err)
	}
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// 2026-10-14: a K7 test board on its own directory and port, started with
// -allow-parallel, stopped the production board from restarting, and the
// recovery weakened production's own guard. A board that declared itself a
// deliberate second board never blocks another; two undeclared boards still
// collide, because that is the silent partition the guard exists for.
func TestADeclaredSecondBoardDoesNotBlockTheProductionBoard(t *testing.T) {
	for _, declared := range []bool{true, false} {
		t.Setenv("HOME", t.TempDir()) // the registry lives under HOME
		fakeBoard(t, 4242424, t.TempDir(), declared)
		release, err := claimHostSlot("127.0.0.1:4777", "", t.TempDir(), false)
		if declared && err != nil {
			t.Fatalf("a declared test board blocked production from starting: %v", err)
		}
		if !declared && err == nil {
			t.Fatal("two undeclared boards started side by side: the guard no longer guards")
		}
		if release != nil {
			release()
		}
	}
}

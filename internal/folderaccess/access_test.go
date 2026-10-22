// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package folderaccess

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestBlockedReadHasOneSlotAndRetainsItsEventualResult(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	var reads atomic.Int64
	m := New(home, func(string) error {
		if reads.Add(1) == 1 {
			close(started)
		}
		<-release
		return nil
	})
	m.Directory(filepath.Join(home, "Desktop", "project"))
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("setup: reader did not start")
	}
	for range 10 {
		m.Directory(filepath.Join(home, "Desktop", "project"))
	}
	if reads.Load() != 1 {
		t.Fatalf("blocked root created %d readers", reads.Load())
	}
	rows := m.Snapshot()
	if len(rows) != 1 || rows[0].State != "pending" {
		t.Fatalf("blocked read was not pending: %+v", rows)
	}
	close(release)
	waitState(t, m, "granted")
	if Advice(m.Snapshot()) != "" {
		t.Fatal("completed read retained a denial diagnosis")
	}
}

func TestDeniedReadRequiresAnExplicitRetry(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var reads atomic.Int64
	m := New(home, func(string) error { reads.Add(1); return os.ErrPermission })
	m.Directory(filepath.Join(home, "Documents", "project"))
	waitState(t, m, "denied")
	for range 10 {
		m.Directory(filepath.Join(home, "Documents", "project"))
	}
	if reads.Load() != 1 || Advice(m.Snapshot()) == "" {
		t.Fatalf("denied read was retried automatically or hidden: %d %+v", reads.Load(), m.Snapshot())
	}
	if !m.Request() {
		t.Fatal("explicit acquisition attempt was not requested")
	}
	deadline := time.Now().Add(time.Second)
	for reads.Load() < 4 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if reads.Load() != 4 {
		t.Fatalf("explicit request did not attempt each ungranted root once: %d", reads.Load())
	}
}

func TestRealReadDoesNotNeedADirectoryEntry(t *testing.T) {
	if err := readDirectory(t.TempDir()); err != nil {
		t.Fatal("empty directory is readable:", err)
	}
}

func waitState(t *testing.T, m *Monitor, state string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		for _, row := range m.Snapshot() {
			if row.State == state {
				return
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("state %q was not reached: %+v", state, m.Snapshot())
}

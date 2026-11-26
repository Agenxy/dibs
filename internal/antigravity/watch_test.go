// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package antigravity

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHubFilesystemEventsReobserveAndDeduplicate(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".gemini", "antigravity", "daemon")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "hub_fixture.json")
	write := func(value string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("old")
	ready := make(chan struct{})
	changes := make(chan string, 8)
	done := make(chan error, 1)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() {
		done <- watchHub(ctx, home, func() (string, bool) {
			b, err := os.ReadFile(path)
			return string(b), err == nil && len(b) > 0
		}, func(epoch string) { changes <- epoch }, func() { close(ready) })
	}()
	select {
	case <-ready:
	case err := <-done:
		t.Fatal("watch setup", err)
	case <-time.After(5 * time.Second):
		t.Fatal("no watcher baseline")
	}
	expect := func(want string) {
		t.Helper()
		select {
		case got := <-changes:
			if got != want {
				t.Fatalf("epoch=%q want %q", got, want)
			}
		case err := <-done:
			t.Fatalf("watcher stopped before %q: %v", want, err)
		case <-time.After(5 * time.Second):
			t.Fatal("filesystem change never reached observer")
		}
	}
	write("new")
	expect("new")
	write("new")
	select {
	case got := <-changes:
		t.Fatalf("unchanged epoch was reoffered: %s", got)
	case <-time.After(150 * time.Millisecond):
	}
	// Replace the directory inode, not just a descriptor's bytes.
	retired := dir + "-retired"
	if err := os.Rename(dir, retired); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	write("returned")
	expect("returned")
	write("next")
	expect("next")
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("watcher ignored cancellation")
	}
}

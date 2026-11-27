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

func TestHubFilesystemWatchFollowsMissingAncestors(t *testing.T) {
	home := t.TempDir()
	file := filepath.Join(home, ".gemini", "antigravity", "daemon", "hub.json")
	ready, changed, done := make(chan struct{}), make(chan string, 2), make(chan error, 1)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() {
		done <- watchHub(ctx, home, func() (string, bool) {
			b, err := os.ReadFile(file)
			return string(b), err == nil && len(b) > 0
		}, func(epoch string) { changed <- epoch }, func() { close(ready) })
	}()
	select {
	case <-ready:
	case err := <-done:
		t.Fatal("watch setup", err)
	case <-time.After(5 * time.Second):
		t.Fatal("watcher never established missing-directory baseline")
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, epoch := range []string{"first", "next"} {
		if err := os.WriteFile(file, []byte(epoch), 0o600); err != nil {
			t.Fatal(err)
		}
		select {
		case got := <-changed:
			if got != epoch {
				t.Fatalf("got %q want %q", got, epoch)
			}
		case err := <-done:
			t.Fatal("watcher stopped", err)
		case <-time.After(5 * time.Second):
			t.Fatal("new descriptor tree did not produce an observation")
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

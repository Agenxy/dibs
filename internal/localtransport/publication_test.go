// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

//go:build darwin || linux

package localtransport

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// Observe the actual filesystem while the real Listen is running. A widened
// bind-to-chmod window must expose the old bug here; testing only after Listen
// returns would pass even when a real bridge permanently refused startup.
func TestListenPublishesOnlySecuredFinalSocket(t *testing.T) {
	dir := privateDir(t)
	for range 16 {
		result := make(chan struct {
			ln  net.Listener
			err error
		}, 1)
		go func() {
			ln, err := Listen(dir)
			result <- struct {
				ln  net.Listener
				err error
			}{ln, err}
		}()
		deadline := time.NewTimer(3 * time.Second)
		var observed error
		for {
			select {
			case r := <-result:
				deadline.Stop()
				if r.err != nil {
					t.Fatal(r.err)
				}
				if err := checkSocket(Path(dir)); err != nil && observed == nil {
					observed = err
				}
				if r.ln.Addr().String() != Path(dir) && observed == nil {
					observed = fmt.Errorf("logical listener address=%s, want %s", r.ln.Addr(), Path(dir))
				}
				if err := r.ln.Close(); err != nil {
					t.Fatal(err)
				}
				if observed != nil {
					t.Fatalf("final socket was published before it was secured: %v", observed)
				}
				goto next
			case <-deadline.C:
				// Collect before failing; a fixture must not leave a publisher
				// writing into a directory that TempDir is deleting.
				r := <-result
				if r.ln != nil {
					_ = r.ln.Close()
				}
				t.Fatal("Listen did not finish within the bounded observation")
			default:
				if _, err := os.Lstat(Path(dir)); err == nil {
					if err := checkSocket(Path(dir)); err != nil && observed == nil {
						observed = err
					}
				} else if !errors.Is(err, os.ErrNotExist) && observed == nil {
					observed = err
				}
			}
		}
	next:
	}
}

func TestConcurrentListenPublishesExactlyOneEndpoint(t *testing.T) {
	dir := privateDir(t)
	original := publishSocket
	t.Cleanup(func() { publishSocket = original })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var arrived atomic.Int32
	release := make(chan struct{})
	publishSocket = func(from, into string) error {
		if arrived.Add(1) == 2 {
			close(release)
		}
		select {
		case <-release:
			return original(from, into)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	type outcome struct {
		ln  net.Listener
		err error
	}
	results := make(chan outcome, 2)
	for range 2 {
		go func() { ln, err := Listen(dir); results <- outcome{ln, err} }()
	}
	winners, collisions := []net.Listener{}, 0
	defer func() {
		for _, ln := range winners {
			_ = ln.Close()
		}
	}()
	for range 2 {
		r := <-results
		if r.ln != nil {
			winners = append(winners, r.ln)
		}
		if errors.Is(r.err, syscall.EEXIST) {
			collisions++
		} else if r.err != nil {
			t.Errorf("unexpected publication error: %v", r.err)
		}
	}
	if arrived.Load() != 2 {
		t.Fatalf("both real Listen calls must reach the held publication boundary, got %d", arrived.Load())
	}
	if len(winners) != 1 || collisions != 1 {
		t.Fatalf("concurrent publication replaced an endpoint: winners=%d exclusive collisions=%d", len(winners), collisions)
	}
	if err := checkSocket(Path(dir)); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != SocketName {
		t.Fatalf("losing publisher left preparation nodes: %v", entries)
	}
}

func TestFailedSocketPublicationRemovesOnlyItsPreparation(t *testing.T) {
	dir := privateDir(t)
	original := publishSocket
	t.Cleanup(func() { publishSocket = original })
	failure := errors.New("fixture publication refused")
	publishSocket = func(string, string) error { return failure }
	ln, err := Listen(dir)
	if ln != nil {
		_ = ln.Close()
		t.Fatal("failed publication returned a listener")
	}
	if !errors.Is(err, failure) {
		t.Fatalf("lost publication error: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("failed publication leaked preparation nodes: %v", entries)
	}
}

func TestSocketPreparationPreservesUsableProfilePathLength(t *testing.T) {
	base := privateDir(t)
	// A 100-byte final pathname fits Darwin's 104-byte sockaddr_un as well as
	// Linux's. An ordinary long random temp basename would break both here.
	dir := filepath.Join(base, strings.Repeat("p", 90-len(base)-1))
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	ln, err := Listen(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("closed listener leaked nodes: %v", entries)
	}
}

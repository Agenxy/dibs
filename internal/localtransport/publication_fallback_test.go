// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

//go:build darwin || linux

package localtransport

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestListenFallbackOnlyForUnsupportedExclusiveRename(t *testing.T) {
	for _, tc := range []struct {
		name     string
		fault    error
		fallback bool
	}{
		{"invalid-flags", syscall.EINVAL, true},
		{"missing-syscall", syscall.ENOSYS, true},
		{"io-error", syscall.EIO, false},
		{"permission", syscall.EACCES, false},
		{"operation-denied", syscall.EPERM, false},
		{"missing-source", syscall.ENOENT, false},
		{"existing-target", syscall.EEXIST, false},
		{"other-unsupported", syscall.ENOTSUP, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := privateDir(t)
			original := publishSocket
			t.Cleanup(func() { publishSocket = original })
			var temporary string
			publishSocket = func(from, _ string) error { temporary = from; return tc.fault }
			ln, err := Listen(dir)
			if !tc.fallback {
				if ln != nil {
					_ = ln.Close()
					t.Fatal("a non-portability error fell back and published")
				}
				if !errors.Is(err, tc.fault) {
					t.Fatalf("lost original refusal: %v", err)
				}
				entries, e := os.ReadDir(dir)
				if e != nil {
					t.Fatal(e)
				}
				if len(entries) != 0 {
					t.Fatalf("refusal leaked nodes: %v", entries)
				}
				return
			}
			if err != nil {
				t.Fatalf("unsupported rename did not publish through fallback: %v", err)
			}
			defer func() { _ = ln.Close() }()
			if temporary == "" {
				t.Fatal("setup: real Listen did not enter native publication boundary")
			}
			if _, err := os.Lstat(temporary); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("fallback kept its preparation alias: %v", err)
			}
			if err := checkSocket(Path(dir)); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			conn, err := Dial(ctx, dir)
			if err != nil {
				t.Fatalf("published socket hard link is not usable: %v", err)
			}
			_ = conn.Close()
		})
	}
}

func TestListenFallbackNeverReplacesAppearingEndpoint(t *testing.T) {
	dir := privateDir(t)
	original := publishSocket
	t.Cleanup(func() { publishSocket = original })
	var before os.FileInfo
	publishSocket = func(_, into string) error {
		if err := os.WriteFile(into, []byte("concurrent endpoint"), 0o600); err != nil {
			return err
		}
		var err error
		before, err = os.Lstat(into)
		if err != nil {
			return err
		}
		return syscall.EINVAL
	}
	ln, err := Listen(dir)
	if ln != nil {
		_ = ln.Close()
		t.Fatal("fallback replaced the concurrent endpoint")
	}
	if !errors.Is(err, syscall.EEXIST) {
		t.Fatalf("fallback did not refuse existing target: %v", err)
	}
	after, err := os.Lstat(Path(dir))
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(Path(dir))
	if err != nil {
		t.Fatal(err)
	}
	if before == nil || !os.SameFile(before, after) || string(body) != "concurrent endpoint" {
		t.Fatal("fallback mutated the appearing endpoint")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("losing fallback leaked preparation: %v", entries)
	}
}

func TestStrayUnpublishedSocketDoesNotBlockProfileStartup(t *testing.T) {
	dir := privateDir(t)
	strayPath := filepath.Join(dir, ".STRAY234")
	stray, err := net.ListenUnix("unix", &net.UnixAddr{Name: strayPath, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	stray.SetUnlinkOnClose(false)
	if err := stray.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.Lstat(strayPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckDir(dir, false); err != nil {
		t.Fatalf("stray socket blocked authority checks: %v", err)
	}
	ln, err := Listen(dir)
	if err != nil {
		t.Fatalf("stray socket blocked startup: %v", err)
	}
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	after, err := os.Lstat(strayPath)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("startup removed an unknown leftover: %v", err)
	}
}

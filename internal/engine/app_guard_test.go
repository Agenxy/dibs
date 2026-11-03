// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/codexipc"
	"github.com/agenxy/dibs/internal/harnessenv"
	"github.com/agenxy/dibs/internal/paths"
	"github.com/agenxy/dibs/internal/procexec"
)

func TestMain(m *testing.M) {
	// Keep tool caches where the caller selected them while isolating every
	// harness/profile lookup, including os.UserCacheDir's desktop-wide lock.
	home, err := os.UserHomeDir()
	if err != nil {
		panic(err)
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		panic(err)
	}
	for key, value := range map[string]string{"GOPATH": filepath.Join(home, "go"), "GOCACHE": filepath.Join(cache, "go-build")} {
		if os.Getenv(key) == "" {
			if err := os.Setenv(key, value); err != nil {
				panic(err)
			}
		}
	}
	if err := os.Setenv("DIBS_TEST_FORBID_APP_OPEN", "1"); err != nil {
		panic(err)
	}
	dir, err := os.MkdirTemp("", "dibs-engine-codex-")
	if err != nil {
		panic(err)
	}
	if err := os.Setenv("CODEX_HOME", dir); err != nil {
		panic(err)
	}
	for key, value := range map[string]string{
		"HOME": dir, "USERPROFILE": dir, "XDG_CACHE_HOME": filepath.Join(dir, "cache"), "LOCALAPPDATA": filepath.Join(dir, "cache"),
	} {
		if err := os.Setenv(key, value); err != nil {
			panic(err)
		}
	}
	guard := harnessenv.Shower{
		Holds: func(string) bool { panic("unfaked app access in engine test") },
		Open:  func([]string) error { panic("unfaked app access in engine test") },
	}
	harnessenv.RealShower = guard
	shower = &guard
	// No app: its IPC socket is absent, and the post-restart load does not
	// wait for one. Tests that exercise the load replace both.
	appThreadOwned = func(context.Context, string) (bool, error) { return false, codexipc.ErrNoSocket }
	appReturnReady = 0
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// Enter a fresh package process while another producer holds its inherited
// desktop lock. No part of this probe opens the person's actual cache.
func TestEnginePackageKeepsBackgroundLockPrivate(t *testing.T) {
	if os.Getenv("DIBS_TEST_INHERITED_DESKTOP_LOCK") == "1" {
		app := &fakeApp{}
		app.install(t)
		opened, err := shower.Show(harnessenv.ChatGPTOpenArgv("private-profile"), "private-profile")
		if err != nil || !opened || len(app.opened) != 1 {
			t.Fatalf("package used inherited desktop lock: opened=%t calls=%d err=%v", opened, len(app.opened), err)
		}
		return
	}
	home, cache := t.TempDir(), t.TempDir()
	// os.UserCacheDir uses HOME/Library/Caches on Darwin, XDG_CACHE_HOME on
	// Unix, and LOCALAPPDATA on Windows. Hold all three private possibilities.
	for _, root := range []string{cache, filepath.Join(home, "Library", "Caches")} {
		dir := filepath.Join(root, "dibs")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		f, err := os.OpenFile(filepath.Join(dir, "background-pair.lock"), os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { paths.Unlock(f); _ = f.Close() })
		if err := paths.LockExclusive(f, false); err != nil {
			t.Fatal("setup inherited lock:", err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := procexec.Command(ctx, os.Args[0], "-test.run=^TestEnginePackageKeepsBackgroundLockPrivate$")
	cmd.Env = append(os.Environ(), "DIBS_TEST_INHERITED_DESKTOP_LOCK=1", "HOME="+home, "USERPROFILE="+home,
		"XDG_CACHE_HOME="+cache, "LOCALAPPDATA="+cache)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("package isolation: %v\n%s", err, out)
	}
}

func TestAppGuardRejectsUnfakedAccess(t *testing.T) {
	if route := os.Getenv("DIBS_TEST_UNFAKED_APP"); route != "" {
		if route == "package" {
			shower.Holds("fixture")
		} else {
			_ = harnessenv.RealShower.Open([]string{"/nonexistent/dibs-test-app-guard"})
		}
		t.Fatal("unfaked app guard returned")
	}
	for _, route := range []string{"package", "real"} {
		cmd := exec.Command(os.Args[0], "-test.run=^TestAppGuardRejectsUnfakedAccess$") // this test binary only
		cmd.Env = append(os.Environ(), "DIBS_TEST_UNFAKED_APP="+route)
		out, err := cmd.CombinedOutput()
		if err == nil || !strings.Contains(string(out), "unfaked app access in engine test") {
			t.Fatalf("guard %s did not reject accidental app access: %v %s", route, err, out)
		}
	}
}

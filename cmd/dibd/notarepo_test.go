// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// A directory that is not a git checkout, or no longer exists, is not a tree
// the daemon cannot read. Production `dibs doctor` listed eight trees as
// "cannot be read by the daemon" with a hint about macOS folder access, and
// every one of them was readable: ~/Desktop, project folders that hold
// checkouts rather than being one, a deleted worktree, and `/`. The hint
// sent the operator to System Settings for a grant they had already given.
func TestADirectoryThatIsNotACheckoutIsNotReportedUnreadable(t *testing.T) {
	eng, ctx := testEngine(t)
	plain, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	gone := filepath.Join(plain, "removed-worktree")
	if _, err := eng.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "a", Agent: &core.AgentInfo{CWD: plain}}); err != nil {
		t.Fatalf("setup: register: %v", err)
	}
	if got := eng.WorkingDirectories(ctx); !slices.Contains(got, plain) {
		t.Fatalf("setup: WorkingDirectories = %v, want %s", got, plain)
	}

	f := defaultScorerFlags()
	f.indexKnownTrees(ctx, eng)         // the boot path
	<-f.indexDiscovered(ctx, eng, gone) // the registration path

	if got := eng.MatchStatus().Unreadable; len(got) != 0 {
		t.Fatalf("unreadable = %v: a plain directory and a missing one were reported as trees the daemon cannot read", got)
	}

	// An earlier verdict is retracted once the daemon has read the directory
	// and found it ordinary: a folder grant given after boot must not leave
	// doctor contradicting itself until a restart.
	eng.NoteUnreadableTree(plain, "folder access was still pending")
	<-f.indexDiscovered(ctx, eng, plain)
	if got := eng.MatchStatus().Unreadable; slices.Contains(got, plain) {
		t.Fatalf("unreadable = %v after the daemon read %s and found no checkout", got, plain)
	}
}

// The control: a directory the daemon really cannot enter is still reported,
// so the test above is not passing because nothing is reported at all.
func TestADirectoryTheDaemonCannotEnterIsStillReportedUnreadable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a mode-0 directory")
	}
	eng, ctx := testEngine(t)
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	locked := filepath.Join(base, "locked")
	if err := os.Mkdir(locked, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })

	f := defaultScorerFlags()
	<-f.indexDiscovered(ctx, eng, locked)
	if got := eng.MatchStatus().Unreadable; !slices.Contains(got, locked) {
		t.Fatalf("unreadable = %v, want the locked directory %s", got, locked)
	}
}

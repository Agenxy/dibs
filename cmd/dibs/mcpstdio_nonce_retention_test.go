// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/procinfo"
)

func TestRegisterCompactsScopedNonceCacheAndRetainsRecovery(t *testing.T) {
	dir, project := t.TempDir(), t.TempDir()
	t.Setenv("DIBS_DIR", dir)
	project, err := filepath.EvalSymlinks(project)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, scopedNonceStore)
	active := nonceCacheName("worker", "codex-thread:current")
	idle := nonceCacheName("worker", "codex-thread:idle")
	held := nonceCacheName("legacy", "codex-thread:legacy-owner")
	unknown := nonceCacheName("worker", "codex-thread:before-tracking")
	old := time.Now().AddDate(-1, 0, 0).Unix()
	oldThread := fmt.Sprintf(`{"kind":"codex-thread","last_used":%d}`, old)
	ownerKey := "owner:v1:" + canonicalNonceKey(project, "legacy")
	before := map[string]string{
		canonicalNonceKey(project, active):  "current-fixture",
		nonceKey(project, active):           "current-fixture",
		"scope:v1:" + active:                oldThread,
		canonicalNonceKey(project, idle):    "expired-fixture",
		"scope:v1:" + idle:                  oldThread,
		canonicalNonceKey(project, held):    "legacy-fixture",
		"scope:v1:" + held:                  oldThread,
		ownerKey:                            held,
		canonicalNonceKey(project, unknown): "opaque-fixture",
	}
	if err := writeNonces(path, before); err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"name": "worker", "cwd": project}
	if err := enrichNonce(args, "", "codex-thread:current"); err != nil {
		t.Fatal(err)
	}
	if args["nonce"] != "current-fixture" {
		t.Fatal("registration lost the active scope's cached nonce")
	}
	after, err := readNonceStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if after[canonicalNonceKey(project, idle)] != "" || after["scope:v1:"+idle] != "" {
		t.Error("expired scoped credentials stayed in the cache")
	}
	if after[nonceKey(project, active)] != "" {
		t.Error("redundant exact-path copy stayed in the cache")
	}
	if after[ownerKey] != held || after[canonicalNonceKey(project, held)] != "legacy-fixture" {
		t.Error("compaction discarded the legacy ownership fence or its credential")
	}
	if after[canonicalNonceKey(project, unknown)] != "opaque-fixture" {
		t.Error("compaction guessed the age of an opaque pre-upgrade scope")
	}
}

func TestRegisterPrunesDeadBridgeScopesAndRetainsTheLiveIncarnation(t *testing.T) {
	if !procinfo.Native {
		t.Skip("this host has no native process evidence")
	}
	started, err := procinfo.Start(os.Getpid())
	if err != nil {
		t.Fatal("observe test process:", err)
	}
	dir, project := t.TempDir(), t.TempDir()
	t.Setenv("DIBS_DIR", dir)
	path := filepath.Join(dir, scopedNonceStore)
	liveScope := fmt.Sprintf("bridge-process:%d:%s", os.Getpid(), procinfo.Stamp(started))
	reusedScope := fmt.Sprintf("bridge-process:%d:%s", os.Getpid(), procinfo.Stamp(started.Add(-time.Hour)))
	live := nonceCacheName("worker", liveScope)
	reused := nonceCacheName("worker", reusedScope)
	now := time.Now()
	before := map[string]string{
		canonicalNonceKey(project, live):   "still-live-fixture",
		canonicalNonceKey(project, reused): "earlier-incarnation-fixture",
	}
	noteNonceScopeUse(before, live, liveScope, now.AddDate(-1, 0, 0))
	noteNonceScopeUse(before, reused, reusedScope, now)
	if err := writeNonces(path, before); err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"name": "worker", "cwd": project}
	if err := enrichNonce(args, "", "codex-thread:current"); err != nil {
		t.Fatal(err)
	}
	after, err := readNonceStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if after[canonicalNonceKey(project, live)] != "still-live-fixture" {
		t.Error("expired observation time overrode the kernel's live process evidence")
	}
	if after[canonicalNonceKey(project, reused)] != "" || after[nonceScopePrefix+reused] != "" {
		t.Error("a reused PID shielded a process scope from compaction")
	}
}

func TestRegisterExpiresUnseenOpaqueScopesAfterMigrationGrace(t *testing.T) {
	dir, project := t.TempDir(), t.TempDir()
	t.Setenv("DIBS_DIR", dir)
	path := filepath.Join(dir, scopedNonceStore)
	active := nonceCacheName("worker", "codex-thread:current")
	old := nonceCacheName("worker", "codex-thread:never-returned")
	if err := writeNonces(path, map[string]string{
		canonicalNonceKey(project, active): "returning-fixture",
		canonicalNonceKey(project, old):    "retired-fixture",
		nonceScopeFirstSweep:               fmt.Sprint(time.Now().AddDate(-1, 0, 0).Unix()),
	}); err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"name": "worker", "cwd": project}
	if err := enrichNonce(args, "", "codex-thread:current"); err != nil {
		t.Fatal(err)
	}
	after, err := readNonceStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if args["nonce"] != "returning-fixture" || after[canonicalNonceKey(project, active)] != "returning-fixture" {
		t.Error("the current registration lost its pre-upgrade nonce")
	}
	if after[canonicalNonceKey(project, old)] != "" {
		t.Error("an unused scope outlived its migration grace period")
	}
}

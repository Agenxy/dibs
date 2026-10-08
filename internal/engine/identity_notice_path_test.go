// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

func identityNoticeCalls(t *testing.T) (func() int, func(string, string)) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	e := New(core.NewState("notice-local", core.DefaultLimits()), &memLedger{}, deadProber{})
	done := make(chan struct{})
	go func() { e.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "boss", Nonce: "boss-secret", Agent: &core.AgentInfo{CWD: "/elsewhere", HostID: "notice-local"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpGrantRole, To: "boss", Mode: core.RoleCoordinator, RoleByHuman: true}); err != nil {
		t.Fatal(err)
	}
	e.SetUnidentifiedPolicy("coordinator")
	count := func() int {
		t.Helper()
		r, err := e.query(ctx, func() core.Result { return core.Result{"count": len(e.takeNotices("boss"))} })
		if err != nil {
			t.Fatal(err)
		}
		return r["count"].(int)
	}
	poll := func(cwd, host string) {
		t.Helper()
		if _, err := e.HookPollFrom(ctx, "", "Stop", cwd, host, false, false); err != nil {
			t.Fatal(err)
		}
	}
	return count, poll
}

func TestIdentityNoticeHostThroughHookPoll(t *testing.T) {
	count, poll := identityNoticeCalls(t)
	poll("/remote/repo", "remote-one")
	before := count()
	if before != 1 {
		t.Fatalf("setup: no first notice, got %d", before)
	}
	poll("/remote/repo", "remote-two")
	if count() != before+1 {
		t.Fatal("one host suppressed another host's notice for the same path")
	}
	poll("/remote/repo", "remote-two")
	if count() != before+1 {
		t.Fatal("same host/path was not throttled")
	}
}

func TestIdentityNoticeNativePathThroughHookPoll(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("native case spelling requires Darwin F_GETPATH")
	}
	count, poll := identityNoticeCalls(t)
	root := filepath.Join(t.TempDir(), "Supgang")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(filepath.Dir(root), "SupGang")
	a, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.Stat(alias)
	if os.IsNotExist(err) {
		t.Skip("native alias subcase unavailable on case-sensitive fixture volume")
	}
	if err != nil || !os.SameFile(a, b) {
		t.Fatalf("setup: native alias evidence missing: %v", err)
	}
	poll(root, "notice-local")
	before := count()
	poll(alias, "notice-local")
	if count() != before {
		t.Fatal("native case alias repeated the local notice")
	}
	poll(root, "remote-case")
	before = count()
	poll(alias, "remote-case")
	if count() != before+1 {
		t.Fatal("remote paths were canonicalized against the local filesystem")
	}
}

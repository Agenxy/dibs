// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package notify

import (
	"context"
	"errors"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

func TestBackgroundWakeUsesOnlyTheNonNavigatingCommand(t *testing.T) {
	previous := backgroundWakeFixture
	t.Cleanup(func() { backgroundWakeFixture = previous })
	const url = "codex://threads/wake/01696949-1111-4222-8333-444455556666"
	calls := 0
	backgroundWakeFixture = func(ctx context.Context, argv []string) error {
		calls++
		if _, ok := ctx.Deadline(); !ok || ctx.Err() != nil {
			t.Fatal("unbounded or expired contact")
		}
		if !reflect.DeepEqual(argv, []string{"/usr/bin/open", "-g", url}) {
			t.Fatalf("unsafe wake argv: %q", argv)
		}
		return nil
	}
	for range 3 {
		if err := OpenChatGPTBackground(url); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 3 {
		t.Fatalf("wake: calls=%d, want 3", calls)
	}
}

func TestBackgroundWakeRejectsNavigationBeforeAnyContact(t *testing.T) {
	previous := backgroundWakeFixture
	t.Cleanup(func() { backgroundWakeFixture = previous })
	calls := 0
	backgroundWakeFixture = func(context.Context, []string) error { calls++; return nil }
	for _, url := range []string{
		"codex://threads/01696949-1111-4222-8333-444455556666", "codex://threads/new",
		"claude://code/continue?session=local_fixture", "codex://threads/wake/not-a-uuid",
		"codex://threads/wake/01696949-1111-4222-8333-444455556666?extra=true",
	} {
		if err := OpenChatGPTBackground(url); err == nil {
			t.Errorf("navigation accepted: %q", url)
		}
	}
	if calls != 0 {
		t.Fatalf("refused routes prepared %d app contacts", calls)
	}
}

func TestBackgroundWakeFailureNeverRetriesOrSelectsAnotherRoute(t *testing.T) {
	previous := backgroundWakeFixture
	t.Cleanup(func() { backgroundWakeFixture = previous })
	for _, failure := range []error{context.DeadlineExceeded, exec.ErrWaitDelay, errors.New("unavailable")} {
		calls := 0
		backgroundWakeFixture = func(context.Context, []string) error { calls++; return failure }
		err := OpenChatGPTBackground("codex://threads/wake/01696949-1111-4222-8333-444455556666")
		if calls != 1 || !errors.Is(err, failure) || !strings.Contains(err.Error(), "mail remains queued") {
			t.Fatalf("failure retried or lost its uncertainty: %d %v", calls, err)
		}
	}
}

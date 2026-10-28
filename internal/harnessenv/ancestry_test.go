// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package harnessenv

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestAncestryUsesOneBoundedInventory(t *testing.T) {
	original := appProbeOutput
	t.Cleanup(func() { appProbeOutput = original })
	calls := 0
	appProbeOutput = func(ctx context.Context, binary string, args ...string) ([]byte, error) {
		calls++
		if _, bounded := ctx.Deadline(); !bounded || binary != "/bin/ps" ||
			strings.Join(args, " ") != "-axo pid=,ppid=,lstart=,comm=" {
			t.Fatalf("ancestry probe lacks the shared bounded inventory: %s %v", binary, args)
		}
		return []byte("301 200 Sun Oct 4 03:10:00 2026 /bin/dibs\n" +
			"200 100 Sun Oct 4 03:09:59 2026 /Applications/ChatGPT.app/runtime\n" +
			"100 1 Sun Oct 4 03:09:57 2026 /Applications/ChatGPT.app/Contents/MacOS/ChatGPT\n"), nil
	}
	want := []string{"/Applications/ChatGPT.app/runtime", "/Applications/ChatGPT.app/Contents/MacOS/ChatGPT"}
	if got := Ancestors(301, 6); calls != 1 || !reflect.DeepEqual(got, want) {
		t.Fatalf("ancestry=%q calls=%d, want %q and one inventory", got, calls, want)
	}
}

func TestAncestryInventoryDeadlineStaysUnknown(t *testing.T) {
	original := appProbeOutput
	t.Cleanup(func() { appProbeOutput = original })
	appProbeOutput = func(ctx context.Context, _ string, _ ...string) ([]byte, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	start := time.Now()
	surface, err := DetectContext(context.Background(), 301)
	if err == nil || surface != "" || time.Since(start) > 3*time.Second {
		t.Fatalf("inventory timeout lost its bound or became evidence: surface=%q err=%v elapsed=%s", surface, err, time.Since(start))
	}
}

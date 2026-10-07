package notify

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Compile the shipped helper's interruption policy, then exercise it without
// asking macOS to show a banner. The production posting path calls the same
// function; the Go helper test proves it receives the matching environment.
func TestNativePassivePriorityRequestsTimeSensitive(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS notification helper")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	binary := filepath.Join(t.TempDir(), "dibs-notify")
	build := exec.CommandContext(ctx, "swiftc", "-D", "DIBS_PRIORITY_FIXTURE", "-o", binary, "app/notify_darwin.swift")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("native priority fixture build: %v\n%s", err, out)
	}
	for _, tc := range []struct{ requested, want string }{{"", "active"}, {"1", "timeSensitive"}} {
		cmd := exec.CommandContext(ctx, binary, "--priority-fixture") // #nosec G204 -- compiled from checked-in source
		cmd.Env = append(os.Environ(), "DIBS_NOTIFY_TIME_SENSITIVE="+tc.requested)
		out, err := cmd.CombinedOutput()
		if err != nil || strings.TrimSpace(string(out)) != tc.want {
			t.Fatalf("requested %q: %v %q, want %q", tc.requested, err, out, tc.want)
		}
	}
}

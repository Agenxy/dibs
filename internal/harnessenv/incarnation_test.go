package harnessenv

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestBridgeIncarnationMustMatchTheObservedProcessTree(t *testing.T) {
	original := appProbeOutput
	t.Cleanup(func() { appProbeOutput = original })
	const started = "Sun Oct 4 03:10:00 2026"
	rows := "301 200 Sun Oct 4 03:10:00 2026 /bin/dibs\n" +
		"200 100 Sun Oct 4 03:09:59 2026 /Applications/ChatGPT.app/runtime\n" +
		"100 1 Sun Oct 4 03:09:57 2026 /Applications/ChatGPT.app/Contents/MacOS/ChatGPT\n"
	appProbeOutput = func(ctx context.Context, binary string, args ...string) ([]byte, error) {
		if _, bounded := ctx.Deadline(); !bounded || binary != "/bin/ps" || strings.Join(args, " ") != "-axo pid=,ppid=,lstart=,comm=" {
			t.Fatalf("unbounded or changed production probe: %s %v", binary, args)
		}
		return []byte(rows), nil
	}
	if app, ok, err := AppForBridge(301, started); err != nil || !ok || app.PID != 100 || app.Start != "Sun Oct 4 03:09:57 2026" {
		t.Fatalf("did not observe owning app: %+v %v", app, ok)
	}
	if app, ok, _ := AppForBridge(301, "a previous process's start"); ok {
		t.Fatalf("PID reuse was accepted: %+v", app)
	}
	rows = strings.ReplaceAll(rows, "/Applications/ChatGPT.app/runtime", "/Applications/Claude.app/runtime")
	if app, ok, _ := AppForBridge(301, started); ok {
		t.Fatalf("nested harness acquired the outer app cohort: %+v", app)
	}
	appProbeOutput = func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("probe failed") }
	if app, ok, err := AppForBridge(301, started); ok || err == nil {
		t.Fatalf("unavailable process evidence was accepted: %+v", app)
	}
}

func TestAppIncarnationProbeDeadlineIsEnforced(t *testing.T) {
	original := appProbeOutput
	t.Cleanup(func() { appProbeOutput = original })
	appProbeOutput = func(ctx context.Context, _ string, _ ...string) ([]byte, error) {
		if _, bounded := ctx.Deadline(); !bounded {
			return nil, fmt.Errorf("unbounded probe")
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if started := ProcessStart(301); started != "" {
		t.Fatalf("unavailable process has a start time: %q", started)
	}
}

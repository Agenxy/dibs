package engine

import (
	"context"
	"github.com/agenxy/dibs/internal/core"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func init() {
	if os.Getenv("DIBS_LINUX_PROBE_DIAG") == "1" {
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})))
		original := appRestartEpoch.Load().(func() (string, bool))
		appRestartEpoch.Store(func() (string, bool) {
			epoch, known := original()
			slog.Debug("LINUX_DIAG_REAL_EPOCH", "epoch", epoch, "known", known)
			return epoch, known
		})
	}
}
func linuxWakeDiagnostic(t *testing.T, e *Engine) {
	t.Helper()
	if os.Getenv("DIBS_LINUX_PROBE_DIAG") != "1" {
		return
	}
	res, err := e.query(context.Background(), func() core.Result {
		l := e.state.Agents["worker"]
		if l == nil {
			return core.Result{"missing": true}
		}
		return core.Result{"status": l.Status, "wake": e.queueWakeView(l, time.Now())}
	})
	t.Logf("LINUX_DIAG_WAKE_RESULT result=%+v error=%v", res, err)
	epoch, known := currentAppRestartEpoch()
	t.Logf("LINUX_DIAG_WAKE_EPOCH %q known=%t", epoch, known)
}

// This diagnostic is deliberately independent of the wake machinery: a changed
// argv[0] must still run touch before that binary can stand in for codex.
func TestLinuxTouchAliasDiagnostic(t *testing.T) {
	if os.Getenv("DIBS_LINUX_PROBE_DIAG") != "1" {
		t.Skip("private hosted diagnosis")
	}
	d := t.TempDir()
	alias := filepath.Join(d, "codex")
	if err := os.Symlink("/usr/bin/touch", alias); err != nil {
		t.Fatal(err)
	}
	target, linkErr := filepath.EvalSymlinks(alias)
	version, versionErr := exec.Command("/usr/bin/touch", "--version").CombinedOutput()
	c := exec.Command(alias, "queue", "thread-fixture")
	c.Dir = d
	output, runErr := c.CombinedOutput()
	_, markerErr := os.Stat(filepath.Join(d, "queue"))
	t.Logf("LINUX_DIAG_ALIAS target=%q link_error=%v version=%q version_error=%v run_error=%v output=%q marker_error=%v", target, linkErr, version, versionErr, runErr, output, markerErr)
	if runErr != nil || markerErr != nil {
		t.Fatal("the renamed system utility is not a valid queue fixture")
	}
}

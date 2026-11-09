package engine

import (
	"context"
	"github.com/agenxy/dibs/internal/core"
	"log/slog"
	"os"
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

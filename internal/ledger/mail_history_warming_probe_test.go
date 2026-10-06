package ledger

import (
	"context"
	"io"
	"os"
	"runtime"
	"slices"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/engine"
)

// Each arm may append real coordination ops after replay, so it MUST get its
// own byte-identical copy. Setup I/O is outside both arms' replay timing.
func copyHistoryProbeLedger(t *testing.T, source, destination string) string {
	t.Helper()
	in, err := os.Open(source) // #nosec G304 -- hosted fixture only
	if err != nil {
		t.Fatal("setup: paired source:", err)
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600) // #nosec G304 -- hosted fixture only
	if err != nil {
		t.Fatal("setup: paired copy:", err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		t.Fatal("setup: identical paired ledger copy:", err)
	}
	if err := out.Close(); err != nil {
		t.Fatal("setup:", err)
	}
	return destination
}

// Actual engine ops run while the serving-triggered builder warms the view.
// No timing is inferred from codec benchmarks, no writer flag is set by hand.
func measureProductionWarm(t *testing.T, ctx context.Context, eng *engine.Engine, l *Ledger, peak uint64) (float64, uint64, float64) {
	t.Helper()
	var actor, token string
	// Fixture party-00 always survives every workload, including merges.
	if l.mail.Measurement().Records >= 100 {
		actor, token = "party-00", "party-00"
	}
	started := time.Now()
	deadline := time.NewTimer(5 * time.Minute)
	defer deadline.Stop()
	tick := time.NewTicker(2 * time.Millisecond)
	defer tick.Stop()
	var latencies []float64
	var mem runtime.MemStats
	for {
		runtime.ReadMemStats(&mem)
		peak = max(peak, mem.HeapAlloc)
		m := l.mail.Measurement()
		if m.Failed {
			t.Fatal("setup: background builder failed")
		}
		if m.Ready {
			break
		}
		if token != "" {
			// The rate limiter is unrelated to builder latency. Refill through
			// its real writer query before timing an unchanged coordination op.
			if err := eng.SetRateTokens(ctx, actor, 30); err != nil {
				t.Fatal("setup:", err)
			}
			at := time.Now()
			res, err := eng.Do(ctx, &core.Op{Kind: core.OpAckBoard, Token: token})
			latencies = append(latencies, time.Since(at).Seconds())
			if err != nil || res["error"] != nil {
				t.Fatal("setup: real warming coordination op failed:", err, res)
			}
		}
		select {
		case <-deadline.C:
			t.Fatal("warm builder did not finish; writer starvation has not been ruled out")
		case <-tick.C:
		}
	}
	slices.Sort(latencies)
	var p99 float64
	if len(latencies) > 0 {
		p99 = latencies[(len(latencies)-1)*99/100]
	} else if l.mail.Measurement().Records >= 1_000_000 {
		t.Fatal("probe did not measure a coordination op during warm-up")
	}
	t.Logf("HISTORY_WARMING_COORDINATION samples=%d p99_seconds=%.9f", len(latencies), p99)
	return time.Since(started).Seconds(), peak, p99
}

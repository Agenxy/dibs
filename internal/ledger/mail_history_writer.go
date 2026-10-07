package ledger

import (
	"context"
	"io"
	"sync/atomic"
	"time"
)

const (
	historyReadBytes = 64 << 10
	historyBackoff   = 5 * time.Millisecond
)

// This is an impure, derived scheduling hint, never canonical state. Append
// owns the flag for its whole operation, including errors. Reading the flag
// acquires no writer lock and losing it loses no coordination state.
type historyWriterActivity struct {
	busy atomic.Bool
}

// The private atomic observer lets a production-door fixture inspect activity
// inside native Write/Sync without reading atomic storage through reflection.
func (l *Ledger) historyWriterIsBusy() bool { return l.historyWriter.busy.Load() }

func (w *historyWriterActivity) wait(ctx context.Context) error {
	if w == nil || !w.busy.Load() {
		return ctx.Err()
	}
	deadline := time.NewTimer(historyBackoff)
	defer deadline.Stop()
	tick := time.NewTicker(500 * time.Microsecond)
	defer tick.Stop()
	for w.busy.Load() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			// Continuous writers cannot starve the audit builder forever. Allow
			// one bounded read/fold chunk after at most 5ms of yielding.
			return ctx.Err()
		case <-tick.C:
		}
	}
	return ctx.Err()
}

type historyReader struct {
	source io.ReaderAt
	writer *historyWriterActivity
	ctx    context.Context
}

func (r *historyReader) ReadAt(p []byte, offset int64) (int, error) {
	var n int
	for n < len(p) {
		if err := r.writer.wait(r.ctx); err != nil {
			return n, err
		}
		// An in-flight read cannot be recalled. At the acceptance model's
		// 20MiB/s, 64KiB occupies 3.125ms (+0.5ms fixed cost), well below
		// the ~30ms writer bound. The 1MiB parser buffer remains unchanged.
		end := min(n+historyReadBytes, len(p))
		chunk := p[n:end]
		got, err := r.source.ReadAt(chunk, offset+int64(n))
		n += got
		if err != nil {
			return n, err
		}
		if got != len(chunk) {
			return n, io.ErrUnexpectedEOF
		}
	}
	return n, nil
}

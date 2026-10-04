package wakeexec

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/agenxy/dibs/internal/paths"
)

// runQueuedCommand serializes observe -> enqueue -> retain across the daemon
// and every bridge on this board. A process-local mutex allowed both writers
// to observe the same empty queue before either committed its item.
func runQueuedCommand(argv []string, thread, agent, dir string, timeout, grace time.Duration) (bool, []byte) {
	release, err := lockQueueAdmission(thread, timeout)
	if err != nil {
		slog.Warn("could not coordinate admission to the app queue; the wake was not sent",
			"agent", agent, "err", err, "hint", "retry after the other queue writer settles; "+
				"restore write access to the board data directory if it cannot be locked")
		return false, nil
	}
	defer release()
	generation := reconnectGeneration(thread)
	pending, known := false, false
	if nativeQueueRoute(argv) {
		pending, known = observeQueue(argv[0], thread)
	}
	if known && pending {
		// A real app item wins over inference, including after a prompt,
		// receipt expiry or reconnect. Retain it for an unavailable next probe.
		r := readReceipt(thread)
		if r.QueuedAt.IsZero() {
			r.QueuedAt = time.Now().UTC()
		}
		r.Generation = generation
		retainQueueReceipt(thread, r, agent)
	}
	if (known && pending) || (!known && fallbackPending(thread, time.Now())) {
		slog.Debug("a Dibs wake is already pending in the app queue", "agent", agent,
			"observation_known", known)
		return true, nil
	}
	slog.Debug("admitting a wake to the app queue", "agent", agent,
		"observation_known", known)
	queuedAt := time.Now().UTC()
	ok, out := runForOut(argv, agent, dir, timeout, grace)
	if ok {
		retainQueueReceipt(thread, queueReceipt{QueuedAt: queuedAt, Generation: generation}, agent)
	}
	return ok, out
}

// The OS releases this lock on process exit. Never unlink it: replacing a
// locked inode would let the next writer lock a different file. Prompt and
// reconnect records remain separate and never wait for a queue command.
func lockQueueAdmission(thread string, limit time.Duration) (func(), error) {
	path := receiptPath(thread) + ".lock"
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	// #nosec G304 -- receiptPath is the private DataDir plus a hashed thread.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	for {
		if err = paths.LockExclusive(f, false); err == nil {
			return func() { paths.Unlock(f); _ = f.Close() }, nil
		}
		if !paths.LockHeldElsewhere(err) {
			_ = f.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}

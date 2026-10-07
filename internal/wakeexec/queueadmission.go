// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package wakeexec

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/agenxy/dibs/internal/paths"
)

type queueCommandOutcome struct {
	ok          bool
	out         []byte
	contended   bool
	observation QueueObservation
}

// RestartQueueOutcome distinguishes a confirmed queue/admission from lock
// contention, which proves no command was run and permits one bounded retry.
type RestartQueueOutcome struct {
	OK, Retryable bool
}

// RunRestartQueue uses the same serialized native queue admission as ordinary
// wakes, but exposes only the one failure class safe to retry automatically.
func RunRestartQueue(argv []string, agent, dir string) RestartQueueOutcome {
	kind, recognised := "", false
	if nativeQueueRoute(argv) {
		kind, recognised = legacyWakeKind(argv[5])
	}
	if !recognised || kind != KindAppRestart {
		return RestartQueueOutcome{}
	}
	thread, valid := queueTarget(argv)
	if !valid {
		return RestartQueueOutcome{}
	}
	out := runQueuedCommand(argv, thread, agent, dir, Timeout, Grace)
	return RestartQueueOutcome{OK: out.ok, Retryable: out.contended}
}

// runQueuedCommand serializes observe -> enqueue -> retain across the daemon
// and every bridge on this board. A process-local mutex allowed both writers
// to observe the same empty queue before either committed its item.
func runQueuedCommand(argv []string, thread, agent, dir string, timeout, grace time.Duration) queueCommandOutcome {
	observation := QueueObservation{Admission: "not_attempted", Pending: "unknown", At: time.Now().UTC()}
	release, err := lockQueueAdmission(thread, timeout)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			slog.Debug("app queue writer is busy; this wake will retry later", "agent", agent)
			return queueCommandOutcome{contended: true, observation: observation}
		}
		slog.Warn("could not coordinate admission to the app queue; the wake was not sent",
			"agent", agent, "err", err, "hint", "retry after the other queue writer settles; "+
				"restore write access to the board data directory if it cannot be locked")
		return queueCommandOutcome{observation: observation}
	}
	defer release()
	generation := reconnectGeneration(thread)
	pending, known := false, false
	if nativeQueueRoute(argv) {
		probe := observeQueueDetail(argv[0], thread)
		pending, known = probe.pending, probe.known
		observation.IssuedAt = probe.issuedAt
		if !probe.issuedAt.IsZero() {
			observation.AgeSource = "notice"
		}
	}
	observation.At = time.Now().UTC()
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
		observation.Admission = "retained"
		if known {
			observation.Pending = "pending"
		} else {
			observation.ReceiptAt = readReceipt(thread).QueuedAt
		}
		return queueCommandOutcome{ok: true, observation: observation}
	}
	slog.Debug("admitting a wake to the app queue", "agent", agent,
		"observation_known", known)
	queuedAt := time.Now().UTC()
	ok, out := runForOut(queueNoticeAt(argv, queuedAt), agent, dir, timeout, grace)
	if ok {
		retainQueueReceipt(thread, queueReceipt{QueuedAt: queuedAt, Generation: generation}, agent)
		observation.Admission = "accepted"
		observation.IssuedAt, observation.AgeSource = queuedAt, "local_admission"
	} else {
		observation.Admission = "failed"
	}
	// Acceptance is not a post-admission list observation. Do not manufacture
	// pending=true from exit zero, even when the pre-admission list was empty.
	observation.At = time.Now().UTC()
	return queueCommandOutcome{ok: ok, out: out, observation: observation}
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

// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"log/slog"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

type restartWarningCapture struct {
	warnings atomic.Int32
	warned   chan struct{}
}

func (*restartWarningCapture) Enabled(context.Context, slog.Level) bool { return true }
func (h *restartWarningCapture) Handle(_ context.Context, r slog.Record) error {
	if r.Message == "app-restart observation was not recorded" {
		h.warnings.Add(1)
		select {
		case h.warned <- struct{}{}:
		default:
		}
	}
	return nil
}

func TestAppRestartWatcherStillReportsLiveObservationFailure(t *testing.T) {
	previousProbe := appRestartEpoch.Load().(func() (string, bool))
	defer appRestartEpoch.Store(previousProbe)
	appRestartEpoch.Store(func() (string, bool) { return "fixture-app", true })
	capture := &restartWarningCapture{warned: make(chan struct{}, 1)}
	previousLog := slog.Default()
	slog.SetDefault(slog.New(capture))
	defer slog.SetDefault(previousLog)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	e := New(core.NewState("live-error", core.DefaultLimits()), &memLedger{}, nil)
	done := make(chan struct{})
	go func() { e.watchAppRestarts(ctx); close(done) }()
	// A failed writer round-trip is the external edge the live watcher must
	// still report. It is not a cancellation of the watcher itself.
	select {
	case req := <-e.ops:
		req.reply <- reply{err: os.ErrDeadlineExceeded}
	case <-time.After(8 * time.Second):
		t.Fatal("setup: watcher did not query the writer")
	}
	select {
	case <-capture.warned:
	case <-time.After(time.Second):
		t.Fatal("live observation failure was silenced")
	}
	cancel()
	<-done
}
func (h *restartWarningCapture) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *restartWarningCapture) WithGroup(string) slog.Handler      { return h }

// Enter the actual watcher. Its second matching process observation is held
// at the OS boundary until shutdown, so its writer query must see cancellation.
func TestAppRestartWatcherIsQuietWhenShutdownCancelsItsObservation(t *testing.T) {
	previousProbe := appRestartEpoch.Load().(func() (string, bool))
	defer appRestartEpoch.Store(previousProbe)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	entered := make(chan struct{})
	var observations atomic.Int32
	appRestartEpoch.Store(func() (string, bool) {
		if observations.Add(1) == 2 {
			close(entered)
			<-ctx.Done()
		}
		return "fixture-app", true
	})
	capture := new(restartWarningCapture)
	previousLog := slog.Default()
	slog.SetDefault(slog.New(capture))
	defer slog.SetDefault(previousLog)
	e := New(core.NewState("shutdown", core.DefaultLimits()), &memLedger{}, nil)
	done := make(chan struct{})
	go func() { e.watchAppRestarts(ctx); close(done) }()
	select {
	case <-entered:
	case <-time.After(8 * time.Second):
		t.Fatal("setup: watcher did not reach its matching observation")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("watcher did not stop")
	}
	if n := capture.warnings.Load(); n != 0 {
		t.Fatalf("normal shutdown reported %d failed restart observations", n)
	}
}

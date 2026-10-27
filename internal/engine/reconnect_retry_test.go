// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/harnessenv"
)

type reconnectFailureLog struct{ onFailure func() }

func (*reconnectFailureLog) Enabled(context.Context, slog.Level) bool { return true }
func (h *reconnectFailureLog) Handle(_ context.Context, r slog.Record) error {
	if r.Message == "could not invalidate the inferred pending wake after reconnect" {
		h.onFailure()
	}
	return nil
}
func (h *reconnectFailureLog) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *reconnectFailureLog) WithGroup(string) slog.Handler      { return h }

func TestFailedReconnectReceiptRemainsRetryableAfterAWriterStall(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DIBS_DIR", dir)
	blockedPath := filepath.Join(dir, "queued-wakes")
	if err := os.WriteFile(blockedPath, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	e := New(core.NewState("retry", core.DefaultLimits()), &memLedger{}, nil)
	ctx, cancel := context.WithCancel(t.Context())
	joined := make(chan struct{})
	go func() { e.Run(ctx); close(joined) }()
	writerEntered, releaseWriter := make(chan struct{}), make(chan struct{})
	faultEntered, releaseFault := make(chan struct{}), make(chan struct{})
	var releaseWriterOnce, releaseFaultOnce sync.Once
	openWriter := func() { releaseWriterOnce.Do(func() { close(releaseWriter) }) }
	openFault := func() { releaseFaultOnce.Do(func() { close(releaseFault) }) }
	t.Cleanup(func() { openWriter(); openFault(); cancel(); <-joined })
	do := func(op *core.Op) core.Result {
		t.Helper()
		r, err := e.Do(ctx, op)
		if err != nil {
			t.Fatal("setup:", err)
		}
		return r
	}
	do(&core.Op{Kind: core.OpRegister, Name: "worker", SessionID: "01a0696b-8446-7821-a992-9dc7f6a43a28", Agent: &core.AgentInfo{Harness: "Codex", Surface: harnessenv.ChatGPTApp}})
	sender := do(&core.Op{Kind: core.OpRegister, Name: "sender"})["token"].(string)
	do(&core.Op{Kind: core.OpSendMessage, Token: sender, To: "worker", MsgType: core.MsgQuestion, Body: "pending reconnect"})
	previous := slog.Default()
	slog.SetDefault(slog.New(&reconnectFailureLog{onFailure: func() { close(faultEntered); <-releaseFault }}))
	defer slog.SetDefault(previous)
	app := harnessenv.AppIncarnation{PID: 4242, Start: "measured-retry-app"}
	done := make(chan error, 1)
	go func() { done <- e.AppReconnected(ctx, e.HostID(), app) }()
	select {
	case <-faultEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("setup: receipt did not fail at the filesystem edge")
	}
	writerDone := make(chan struct{})
	go func() {
		_, _ = e.query(ctx, func() core.Result { close(writerEntered); <-releaseWriter; return nil })
		close(writerDone)
	}()
	select {
	case <-writerEntered:
	case <-time.After(time.Second):
		t.Fatal("setup: writer stall did not start")
	}
	openFault()
	select {
	case err := <-done:
		t.Fatalf("failed receipt abandoned its retry cleanup while writer remained stalled: %v", err)
	case <-time.After(1200 * time.Millisecond):
	}
	openWriter()
	<-writerDone
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("failed filesystem receipt returned success")
		}
	case <-time.After(time.Second):
		t.Fatal("released writer did not finish retry cleanup")
	}
	if err := os.Remove(blockedPath); err != nil {
		t.Fatal(err)
	}
	if err := e.AppReconnected(ctx, e.HostID(), app); err != nil {
		t.Fatal("retry:", err)
	}
	files, err := filepath.Glob(filepath.Join(blockedPath, "*.reconnect"))
	if err != nil || len(files) != 1 {
		t.Fatalf("retry was silently coalesced without writing its receipt: %v %v", files, err)
	}
}

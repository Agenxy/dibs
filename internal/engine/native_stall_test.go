// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/testcodexipc"
)

func TestNativeAppWriterStallBeforeInputDoesNotStrandMail(t *testing.T) {
	ready, entered, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	finished := make(chan error, 1)
	var e *Engine
	s := testcodexipc.Start(t, "idle", func() {
		<-ready
		go func() {
			_, err := e.query(context.Background(), func() core.Result {
				close(entered)
				<-release
				return nil
			})
			finished <- err
		}()
		<-entered // the real writer is stalled before the input freshness query
	})
	var ctx context.Context
	var sender string
	e, ctx, _, sender = nativeAppEngine(t)
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	nativeAppSend(t, e, ctx, sender)
	close(ready)
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("setup never stalled the writer")
	}
	<-time.After(6 * time.Second) // deliberately beyond the retired 5s deadline
	if len(s.Inputs()) != 0 {
		t.Fatal("input skipped the writer freshness fence")
	}
	close(release)
	if err := <-finished; err != nil {
		t.Fatal("writer setup:", err)
	}
	awaitNative(t, s, 1)
	waitWakeDone(t, e, "worker")
	if len(s.Inputs()) != 1 {
		t.Fatal("writer recovery duplicated the notice")
	}
}

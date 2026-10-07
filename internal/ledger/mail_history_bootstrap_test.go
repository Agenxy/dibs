// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package ledger

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/mailhistory"
)

func TestHistoryBootstrapOwnsPrivateStateAndKeepsLiveCommitAfterS0(t *testing.T) {
	l, live := historyBootFixture(t)
	expected, err := historyStateHash(live)
	if err != nil {
		t.Fatal("setup: canonical board hash", err)
	}
	observed := observePrivateBootHash(t, l, live.Limits)
	// Change only the live objects before the engine starts. A retained State
	// pointer OR a shallow State copy would leak both changes into the shadow.
	live.Messages[3].Body = "private live mutation must not enter the reader"
	live.Agents["worker"].Description = "private live object"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng := engine.New(live, l, nil)
	done := make(chan struct{})
	go func() { eng.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	if _, _, err := eng.SubscribeInfo(ctx, ""); err != nil {
		t.Fatal("setup: real writer", err)
	}
	res, err := eng.Do(ctx, &core.Op{Kind: core.OpSendMessage, Token: "sender-token", To: "worker", MsgType: core.MsgNotify, Body: "live after S0"})
	if err != nil || res["error"] != nil {
		t.Fatal("setup: real live commit", err, res)
	}
	if m := l.MailHistory().Measurement(); m.Started || m.Ready || m.CapturedUnits == 0 || m.QueuedBytes == 0 {
		t.Fatal("live op before serving was not captured behind boot prefix", m)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	srv.Listener = l.MailHistory().ServingListener(ctx, srv.Listener)
	srv.Start()
	defer srv.Close()
	response, err := srv.Client().Get(srv.URL)
	if err != nil {
		t.Fatal("setup: actual HTTP Accept", err)
	}
	_ = response.Body.Close()
	m := awaitBootHistory(t, l.MailHistory())
	if m.BootSerial != 3 || <-observed != expected || m.Conversations != 2 || m.QueuedBytes != 0 || m.BuiltSerial <= 3 {
		t.Fatalf("private S0 fold or ordered live suffix disagrees: %+v expected hash=%x", m, expected)
	}
}

func awaitBootHistory(t *testing.T, index *mailhistory.Index) mailhistory.Measurement {
	t.Helper()
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		m := index.Measurement()
		if m.Failed {
			t.Fatal("background history failed")
		}
		if m.Ready {
			return m
		}
		select {
		case <-timeout.C:
			t.Fatal("background history did not catch up")
		case <-tick.C:
		}
	}
}

func TestHistoryBootstrapPreservesRecordBeyondReaderBuffer(t *testing.T) {
	// Encryption expands this real ledger record beyond the production reader's
	// 1 MiB buffer. Exercise its fallback through Replay and actual Accept.
	l, live := historyBootFixtureWith(t, strings.Repeat("large historical body ", 60000))
	expected, err := historyStateHash(live)
	if err != nil {
		t.Fatal("setup: board canary", err)
	}
	observed := observePrivateBootHash(t, l, live.Limits)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	srv.Listener = l.MailHistory().ServingListener(ctx, srv.Listener)
	srv.Start()
	defer srv.Close()
	res, err := srv.Client().Get(srv.URL)
	if err != nil {
		t.Fatal("setup: serving", err)
	}
	_ = res.Body.Close()
	m := awaitBootHistory(t, l.MailHistory())
	if m.BootSerial != 3 || <-observed != expected || m.Conversations != 1 {
		t.Fatal("large committed record was truncated or folded differently", m)
	}
}

// Test-only encoding enters through the actual native serving fold. No canary
// hash or serialized private board is computed by the production run caller.
func observePrivateBootHash(t *testing.T, l *Ledger, limits core.Limits) <-chan [32]byte {
	t.Helper()
	observed := make(chan [32]byte, 1)
	end, err := l.f.Seek(0, io.SeekCurrent)
	if err != nil {
		t.Fatal("setup: validated boot bytes", err)
	}
	head := mailhistory.Record{Serial: 3, End: end, Hash: l.headSum}
	source := &historyReplay{file: l.f, box: l.box, node: l.nodeID, limits: limits, head: head, records: 3, writer: &l.historyWriter}
	l.mail.ConfigureBootstrap(source.head, source.records, func(ctx context.Context, index *mailhistory.Index) error {
		shadow, last, err := source.fold(ctx, index)
		if err != nil {
			return err
		}
		hash, err := historyStateHash(shadow)
		if err != nil {
			return err
		}
		observed <- hash
		index.ReplayProjector().Finish(last)
		return nil
	})
	return observed
}

func historyStateHash(st *core.State) ([32]byte, error) {
	// encoding/json orders map keys and follows core's existing credential
	// redactions. The canary covers that canonical board encoding without a
	// separately maintained projection of its fields.
	raw, err := json.Marshal(st)
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(raw), nil
}

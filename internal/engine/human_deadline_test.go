// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/humanask"
)

func TestHumanPresenterGetsRecordedRequestDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	n := &cleanupNotifier{started: make(chan humanask.Message, 1), post: make(chan struct{}), removed: make(chan NotificationCleanup, 4)}
	e := New(core.NewState("deadline-node", core.DefaultLimits()), &memLedger{}, deadProber{})
	e.SetHumanNotifier(n)
	go e.Run(ctx)
	defer close(n.post)
	human, _, err := e.HumanAgent(ctx)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "sender", Nonce: "deadline-sender"})
	if err != nil {
		t.Fatal(err)
	}
	before := time.Now()
	sent, err := e.Do(ctx, &core.Op{Kind: core.OpSendMessage, Token: reg["token"].(string), To: human, MsgType: core.MsgRequest, Body: "full request", DeadlineSec: 3600})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case shown := <-n.started:
		notice, found, err := e.HumanNoticeFor(ctx, sent["msg_serial"].(uint64))
		if err != nil || !found || shown.Deadline.IsZero() || !shown.Deadline.Equal(notice.Deadline) || shown.Deadline.Before(before.Add(time.Hour)) {
			t.Fatalf("presenter lost the recorded deadline: shown=%v notice=%v found=%v err=%v", shown.Deadline, notice.Deadline, found, err)
		}
	case <-ctx.Done():
		t.Fatal("actual send never reached the presenter")
	}
}

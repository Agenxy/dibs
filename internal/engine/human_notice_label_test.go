// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	hostdisplay "github.com/agenxy/dibs/internal/hostname"
	"github.com/agenxy/dibs/internal/humanask"
)

func TestHumanNoticeUsesFriendlyHostWithoutChangingReplayableLabel(t *testing.T) {
	friendly := hostdisplay.Name()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	n := &cleanupNotifier{started: make(chan humanask.Message, 1), post: make(chan struct{}), removed: make(chan NotificationCleanup, 4)}
	e := New(core.NewState("local-label-node", core.DefaultLimits()), &memLedger{}, deadProber{})
	e.SetHumanNotifier(n)
	go e.Run(ctx)
	defer close(n.post)
	human, _, err := e.HumanAgent(ctx)
	if err != nil {
		t.Fatal(err)
	}
	r, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "sender", Nonce: "label-fixture", Agent: &core.AgentInfo{HostID: e.HostID(), Host: "old.network.example", Harness: "Codex"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpSendMessage, Token: r["token"].(string), To: human, MsgType: core.MsgQuestion, Body: "question"}); err != nil {
		t.Fatal(err)
	}
	select {
	case shown := <-n.started:
		if !strings.Contains(shown.Who, "at "+friendly) || strings.Contains(shown.Who, "old.network.example") {
			t.Fatalf("native notice used kernel label: %s", shown.Who)
		}
	case <-ctx.Done():
		t.Fatal("send never reached presenter")
	}
	_, err = e.query(ctx, func() core.Result {
		if e.state.Agents["sender"].Agent.Host != "old.network.example" {
			t.Error("display rewrote replayable identity")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/humanask"
)

type failedContactDesktop struct{ posts chan struct{} }

func (failedContactDesktop) Available() bool { return true }
func (n failedContactDesktop) Ask(humanask.Message) (humanask.Answer, error) {
	n.posts <- struct{}{}
	return humanask.Answer{}, errors.New("fixture: notification permission denied")
}

func TestFailedContactPostsRetryOnceAndExposeTheReason(t *testing.T) {
	const session = "d2b03160-6000-4000-8000-000000000002"
	e, _ := contactEngine(t, &core.Agent{Status: core.StatusDormant, SessionID: session, Agent: &core.AgentInfo{Harness: "terminal harness"}})
	posts := make(chan struct{}, 8)
	e.SetHumanNotifier(failedContactDesktop{posts})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpAckBoard, Token: "tok-asker"}); err != nil {
		t.Fatal("setup:", err)
	}
	if _, err := e.HookPoll(ctx, session, "Stop", "", true, false); err != nil {
		t.Fatal("setup Stop:", err)
	}
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpSendMessage, Token: "tok-asker", To: "closed", MsgType: core.MsgQuestion, Body: "private failed-alert body"}); err != nil {
		t.Fatal("setup send:", err)
	}
	for range 2 {
		select {
		case <-posts:
		case <-time.After(8 * time.Second):
			t.Fatal("failed post did not get its one actual retry")
		}
	}
	deadline := time.Now().Add(time.Second)
	for {
		b, err := e.Board(ctx)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(b)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), `"retry_exhausted":true`) && strings.Contains(string(raw), "fixture: notification permission denied") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("failure not visible on board: %s", raw)
		}
		<-time.After(10 * time.Millisecond)
	}
	select {
	case <-posts:
		t.Fatal("a third automatic contact post ran")
	case <-time.After(3 * time.Second):
	}
}

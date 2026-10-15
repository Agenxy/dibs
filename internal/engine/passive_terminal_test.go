// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// k7-dev, 2026-10-14: two expired questions it had already read stayed in
// its UserPromptSubmit digest at every prompt, with nothing it could do to
// clear them. The digest lists mail that still wants the agent, not mail
// that is finished.
func TestAnExpiredQuestionIsNotListedInTheHookDigest(t *testing.T) {
	e := New(core.NewState("digest", core.DefaultLimits()), &memLedger{}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	tok := map[string]string{}
	for _, n := range []string{"asker", "reader"} {
		r, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: n, Nonce: "digest-" + n})
		if err != nil {
			t.Fatal("setup:", err)
		}
		tok[n] = r["token"].(string)
		if _, err := e.Do(ctx, &core.Op{Kind: core.OpAckBoard, Token: tok[n]}); err != nil {
			t.Fatal("setup:", err)
		}
	}
	send := func(body string) uint64 {
		r, err := e.Do(ctx, &core.Op{
			Kind: core.OpSendMessage, Token: tok["asker"], To: "reader",
			MsgType: core.MsgQuestion, Body: body, DeadlineSec: 60,
		})
		if err != nil {
			t.Fatal("setup:", err)
		}
		return r["msg_serial"].(uint64)
	}
	expired := send("EXPIRED-QUESTION")
	live := send("LIVE-QUESTION")
	res, _ := e.query(ctx, func() core.Result {
		e.state.Messages[expired].State = core.MsgStateExpiredDormant
		e.state.Messages[live].Deadline = time.Now().Add(time.Hour)
		return core.Result{"digest": e.passiveMailboxDigest(e.state.Agents["reader"], time.Now())}
	})
	digest, _ := res["digest"].(string)
	if !strings.Contains(digest, fmt.Sprintf("#%d ", live)) {
		t.Fatalf("setup: the waiting question is not in the digest, so this proves nothing: %q", digest)
	}
	if strings.Contains(digest, fmt.Sprintf("#%d ", expired)) {
		t.Fatalf("an expired question is still listed: %q", digest)
	}
}

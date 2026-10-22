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

// agenxy-design, 2026-10-22: a worker finished its request with a
// deliverable, and for about seventy minutes every UserPromptSubmit hook said
// only "2 outstanding update units. updates on #264956, #265039: ack(N) to
// clear without bodies", which reads as bookkeeping. The owner was waiting on
// that deliverable. The Stop hook already names it ("X reports your request
// DONE"); the per-prompt digest and the per-write line must not hide it.
func TestAFinishedRequestIsNamedInThePromptDigestAndWriteLine(t *testing.T) {
	e := New(core.NewState("verdict-digest", core.DefaultLimits()), &memLedger{}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	tok := map[string]string{}
	for _, n := range []string{"owner", "maker"} {
		r, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: n, Nonce: "verdict-" + n})
		if err != nil {
			t.Fatal("setup:", err)
		}
		tok[n] = r["token"].(string)
		if _, err := e.Do(ctx, &core.Op{Kind: core.OpAckBoard, Token: tok[n]}); err != nil {
			t.Fatal("setup:", err)
		}
	}
	sent, err := e.Do(ctx, &core.Op{
		Kind: core.OpSendMessage, Token: tok["owner"], To: "maker",
		MsgType: core.MsgRequest, Body: "make the poster", DeadlineSec: 600,
	})
	if err != nil {
		t.Fatal("setup:", err)
	}
	req := sent["msg_serial"].(uint64)
	for _, op := range []*core.Op{
		{Kind: core.OpRespond, Token: tok["maker"], MsgSerial: req, Disposition: "approve"},
		{
			Kind: core.OpRespond, Token: tok["maker"], MsgSerial: req, Disposition: "done",
			Body: "poster attached", Deliverable: "design/poster-v3.pdf",
		},
	} {
		if r, err := e.Do(ctx, op); err != nil || r["error"] != nil {
			t.Fatal("setup:", op.Disposition, err, r)
		}
	}
	res, _ := e.query(ctx, func() core.Result {
		owner := e.state.Agents["owner"]
		return core.Result{
			"digest": e.passiveMailboxDigest(owner, time.Now()),
			"write":  e.waiting(owner.ID, time.Now()),
		}
	})
	digest, _ := res["digest"].(string)
	write, _ := res["write"].(string)
	t.Logf("prompt digest:\n%s\nwrite line: %s", digest, write)
	if !strings.Contains(digest, fmt.Sprintf("#%d", req)) || !strings.Contains(write, fmt.Sprintf("#%d", req)) {
		t.Fatalf("setup: the finished request is not outstanding, so this proves nothing:\n%s\n%s", digest, write)
	}
	want := fmt.Sprintf("maker reports your request DONE (msg %d): delivered at design/poster-v3.pdf", req)
	if !strings.Contains(digest, want) {
		t.Fatalf("the prompt digest does not name the finished request and its deliverable:\n%s", digest)
	}
	if !strings.Contains(write, fmt.Sprintf("#%d (DONE)", req)) {
		t.Fatalf("the per-write line does not say the request is DONE:\n%s", write)
	}
}

package engine

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

func TestInlineBudgetLeavesOnlyTheUnquotedPrefixUnread(t *testing.T) {
	e := New(core.NewState("prefix-budget", core.DefaultLimits()), &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go e.Run(ctx)
	do := func(op *core.Op) core.Result {
		t.Helper()
		r, err := e.Do(ctx, op)
		if err != nil {
			t.Fatalf("setup %s: %v", op.Kind, err)
		}
		return r
	}
	tokens := map[string]string{}
	for _, id := range []string{"lead", "worker"} {
		tokens[id] = do(&core.Op{Kind: core.OpRegister, Name: id, Nonce: "prefix-budget-" + id})["token"].(string)
		do(&core.Op{Kind: core.OpAckBoard, Token: tokens[id]})
	}
	parent := do(&core.Op{Kind: core.OpSendMessage, Token: tokens["lead"], To: "worker", MsgType: core.MsgRequest, Body: "work"})["msg_serial"].(uint64)
	do(&core.Op{Kind: core.OpRespond, Token: tokens["worker"], MsgSerial: parent, Disposition: "approve"})
	if _, err := e.GetMessage(ctx, tokens["lead"], parent); err != nil {
		t.Fatal(err)
	}
	var words []string
	for i := 1; i <= 3; i++ {
		body := fmt.Sprintf("report%d-", i) + strings.Repeat("λ", 392)
		words = append(words, body)
		do(&core.Op{Kind: core.OpRespond, Token: tokens["worker"], MsgSerial: parent, Disposition: "progress", Body: body})
	}
	do(&core.Op{Kind: core.OpSendMessage, Token: tokens["worker"], To: "lead", MsgType: core.MsgNotify, Body: strings.Repeat("μ", 700)})
	first := fmt.Sprint(do(&core.Op{Kind: core.OpAckBoard, Token: tokens["lead"]})["agent_updates"])
	if !strings.Contains(first, words[0]) || !strings.Contains(first, words[1]) || strings.Contains(first, words[2]) || !strings.Contains(first, "trimmed; read_mail") {
		t.Fatalf("shared mail-first rune budget: %s", first)
	}
	_, err := e.query(ctx, func() core.Result {
		m := e.state.Messages[parent]
		if m.OutcomeReadAt != m.Progress[1].Serial {
			t.Errorf("watermark=%d, want fully quoted second event=%d, not pointer=%d", m.OutcomeReadAt, m.Progress[1].Serial, m.Progress[2].Serial)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	second := fmt.Sprint(do(&core.Op{Kind: core.OpAckBoard, Token: tokens["lead"]})["agent_updates"])
	if strings.Contains(second, "report1-") || strings.Contains(second, "report2-") || !strings.Contains(second, words[2]) {
		t.Errorf("prefix skipped/repeated: %s", second)
	}
	third := fmt.Sprint(do(&core.Op{Kind: core.OpAckBoard, Token: tokens["lead"]})["agent_updates"])
	if strings.Contains(third, "reports progress") {
		t.Errorf("already read reports repeated: %s", third)
	}
}

func TestInlineRequestsAreNewestFirstWithoutReadingPointers(t *testing.T) {
	e := New(core.NewState("request-budget", core.DefaultLimits()), &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go e.Run(ctx)
	do := func(op *core.Op) core.Result {
		t.Helper()
		r, err := e.Do(ctx, op)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	tokens := map[string]string{}
	for _, id := range []string{"lead", "worker"} {
		tokens[id] = do(&core.Op{Kind: core.OpRegister, Name: id, Nonce: "request-budget-" + id})["token"].(string)
		do(&core.Op{Kind: core.OpAckBoard, Token: tokens[id]})
	}
	var parents []uint64
	for i := range 2 {
		n := do(&core.Op{Kind: core.OpSendMessage, Token: tokens["lead"], To: "worker", MsgType: core.MsgRequest, Body: "work"})["msg_serial"].(uint64)
		parents = append(parents, n)
		do(&core.Op{Kind: core.OpRespond, Token: tokens["worker"], MsgSerial: n, Disposition: "approve", Body: fmt.Sprintf("body%d-", i) + strings.Repeat("a", 594)})
	}
	do(&core.Op{Kind: core.OpSendMessage, Token: tokens["worker"], To: "lead", MsgType: core.MsgNotify, Body: strings.Repeat("mail", 175)})
	first := fmt.Sprint(do(&core.Op{Kind: core.OpAckBoard, Token: tokens["lead"]})["agent_updates"])
	if strings.Index(first, "body1-") >= strings.Index(first, "body0-") {
		t.Errorf("older request received budget first: %s", first)
	}
	_, err := e.query(ctx, func() core.Result {
		if e.state.Messages[parents[0]].OutcomeReadAt != 0 || e.state.Messages[parents[1]].OutcomeReadAt == 0 {
			t.Error("partial older quote was read or complete newer quote was not")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestInlineCountBoundLeavesUnshownRequestsUnread(t *testing.T) {
	e := New(core.NewState("count-budget", core.DefaultLimits()), &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go e.Run(ctx)
	do := func(op *core.Op) core.Result {
		t.Helper()
		r, err := e.Do(ctx, op)
		if err != nil {
			t.Fatalf("setup %s: %v", op.Kind, err)
		}
		return r
	}
	tokens := map[string]string{}
	for _, id := range []string{"lead", "worker"} {
		tokens[id] = do(&core.Op{Kind: core.OpRegister, Name: id, Nonce: "count-budget-" + id})["token"].(string)
		do(&core.Op{Kind: core.OpAckBoard, Token: tokens[id]})
	}
	var oldest uint64
	for i := range maxInlineOutcomes + 1 {
		parent := do(&core.Op{
			Kind: core.OpSendMessage, Token: tokens["lead"], To: "worker",
			MsgType: core.MsgQuestion, Body: "question",
		})["msg_serial"].(uint64)
		if i == 0 {
			oldest = parent
		}
		do(&core.Op{
			Kind: core.OpRespond, Token: tokens["worker"], MsgSerial: parent,
			Disposition: "answer", Body: fmt.Sprintf("answer-%02d", i),
		})
	}
	first := do(&core.Op{Kind: core.OpAckBoard, Token: tokens["lead"]})["agent_updates"].([]string)
	if len(first) != maxInlineOutcomes || strings.Contains(fmt.Sprint(first), "answer-00") {
		t.Fatalf("count bound or newest-first selection: %v", first)
	}
	if _, err := e.query(ctx, func() core.Result {
		if e.state.Messages[oldest].OutcomeReadAt != 0 {
			t.Error("unshown oldest request was marked read")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	second := do(&core.Op{Kind: core.OpAckBoard, Token: tokens["lead"]})["agent_updates"].([]string)
	if len(second) != 1 || !strings.Contains(second[0], "answer-00") {
		t.Errorf("unshown request was lost/repeated: %v", second)
	}
}

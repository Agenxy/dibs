package engine

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

func TestOverBudgetProgressCollapsesWithoutReadingTheSuffix(t *testing.T) {
	e := New(core.NewState("collapsed-progress", core.DefaultLimits()), &memLedger{}, deadProber{})
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
		tokens[id] = do(&core.Op{Kind: core.OpRegister, Name: id, Nonce: "collapsed-" + id})["token"].(string)
		do(&core.Op{Kind: core.OpAckBoard, Token: tokens[id]})
	}
	parent := do(&core.Op{Kind: core.OpSendMessage, Token: tokens["lead"], To: "worker",
		MsgType: core.MsgRequest, Body: "work"})["msg_serial"].(uint64)
	do(&core.Op{Kind: core.OpRespond, Token: tokens["worker"], MsgSerial: parent, Disposition: "approve"})
	if _, err := e.GetMessage(ctx, tokens["lead"], parent); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 3; i++ {
		body := fmt.Sprintf("report-%d-", i) + strings.Repeat("λ", 691)
		do(&core.Op{Kind: core.OpRespond, Token: tokens["worker"], MsgSerial: parent,
			Disposition: "progress", Body: body})
	}
	// Real pending mail consumes 900 of the shared 1600-rune budget. Only
	// the first 700-rune progress body fits; the other two stay unread.
	for _, size := range []int{700, 200} {
		do(&core.Op{Kind: core.OpSendMessage, Token: tokens["worker"], To: "lead",
			MsgType: core.MsgNotify, Body: strings.Repeat("μ", size)})
	}
	lines := do(&core.Op{Kind: core.OpAckBoard, Token: tokens["lead"]})["agent_updates"].([]string)
	want := fmt.Sprintf("+2 more updates on %d: read_mail(%d) has the rest", parent, parent)
	if len(lines) != 2 || !strings.Contains(lines[0], "report-1-") || lines[1] != want {
		t.Fatalf("want one quoted unit and one collapsed line, got %v", lines)
	}
	if _, err := e.query(ctx, func() core.Result {
		m := e.state.Messages[parent]
		if m.OutcomeReadAt != m.Progress[0].Serial {
			t.Errorf("collapsed suffix was read: marker=%d first=%d", m.OutcomeReadAt, m.Progress[0].Serial)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// An explicit full read is still the escape hatch. It consumes the exact
	// retained suffix, rather than a summary pretending to be a receipt.
	if _, err := e.GetMessage(ctx, tokens["lead"], parent); err != nil {
		t.Fatal(err)
	}
	next := fmt.Sprint(do(&core.Op{Kind: core.OpAckBoard, Token: tokens["lead"]})["agent_updates"])
	if strings.Contains(next, "updates on") || strings.Contains(next, "reports progress") {
		t.Errorf("read suffix repeated: %s", next)
	}
}

func TestCollapsedSummaryPreservesTheSelectedUnitLimit(t *testing.T) {
	e := New(core.NewState("collapsed-limit", core.DefaultLimits()), &memLedger{}, deadProber{})
	group := outcomeGroup{agent: "lead", message: &core.Message{Serial: 1}}
	for i := range maxInlineOutcomes + 3 {
		group.units = append(group.units, outcomeUnit{serial: uint64(i + 2), text: "progress", body: "words"})
	}
	budget := 0
	lines, through := e.presentGroupedOutcomes([]outcomeGroup{group, {
		agent: "other", message: &core.Message{Serial: 0},
		units: []outcomeUnit{{serial: 1, text: "older", body: "words"}},
	}}, &budget, nil)
	want := fmt.Sprintf("%d unquoted updates on 1: read_mail(1) has the rest", maxInlineOutcomes)
	if len(lines["lead"]) != 1 || lines["lead"][0] != want || len(lines["other"]) != 0 || len(through) != 0 {
		t.Fatalf("summary enlarged the unit allowance or read an omitted unit: %v %v", lines, through)
	}
}

func TestCollapsedSummaryCannotReadAcrossAnUnselectedOrPartialUnit(t *testing.T) {
	for _, tc := range []struct {
		name   string
		budget int
		wanted map[string]bool
		prefix uint64
		lines  int
	}{
		{"partial first quote", 2, nil, 0, 2},
		{"unselected middle", 100, map[string]bool{noticeKey("lead", 2): true, noticeKey("lead", 4): true}, 2, 2},
		{"no quote fits", 0, nil, 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := New(core.NewState("collapsed-gap", core.DefaultLimits()), &memLedger{}, deadProber{})
			group := outcomeGroup{agent: "lead", message: &core.Message{Serial: 1}, units: []outcomeUnit{
				{serial: 2, text: "first", body: "first words"},
				{serial: 3, text: "middle", body: "middle words"},
				{serial: 4, text: "last", body: "last words"},
			}}
			lines, prefix, selected := e.presentOutcomeGroup(group, &tc.budget, tc.wanted, maxInlineOutcomes)
			if prefix != tc.prefix || len(lines) != tc.lines || selected > maxInlineOutcomes ||
				!strings.Contains(lines[len(lines)-1], "read_mail(1)") || strings.Contains(fmt.Sprint(lines), "last words") {
				t.Fatalf("unsafe collapsed prefix: %v prefix=%d selected=%d", lines, prefix, selected)
			}
		})
	}
}

package engine

import (
	"context"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/humanask"
)

type priorityHumanNotifier struct{ posted chan humanask.Message }

func (n priorityHumanNotifier) Available() bool { return true }
func (n priorityHumanNotifier) Ask(m humanask.Message) (humanask.Answer, error) {
	m.Receipt("posted")
	n.posted <- m
	return humanask.Answer{}, nil
}

type unavailablePriorityNotifier struct{}

func (unavailablePriorityNotifier) Available() bool { return false }
func (unavailablePriorityNotifier) Ask(humanask.Message) (humanask.Answer, error) {
	panic("unavailable desktop must not be used")
}

func TestHumanHighNotifyCarriesPriorityToDesktop(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	e := New(core.NewState("priority-human", core.DefaultLimits()), &memLedger{}, deadProber{})
	posted := make(chan humanask.Message, 1)
	e.SetHumanNotifier(priorityHumanNotifier{posted: posted})
	go e.Run(ctx)
	human, _, err := e.HumanAgent(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sender := registerAgent(t, e, "priority-sender")
	sent, err := e.Do(ctx, &core.Op{
		Kind: core.OpSendMessage, Token: sender, To: human,
		MsgType: core.MsgNotify, Body: "urgent operations alert", RequestPriority: "high",
	})
	if err != nil || sent["human_route"] != "desktop" {
		t.Fatalf("human send failed before desktop: %v %v", sent, err)
	}
	select {
	case notice := <-posted:
		if notice.Priority != "high" || notice.Type != core.MsgNotify {
			t.Fatalf("desktop lost priority: %+v", notice)
		}
		pending, err := e.PendingForHuman(ctx)
		if err != nil || len(pending) != 0 {
			t.Fatalf("late relay would duplicate a posted alert: %+v %v", pending, err)
		}
	case <-ctx.Done():
		t.Fatal("high notify was not posted to desktop")
	}
}

func TestHumanHighNotifyCarriesPriorityToRelay(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	e := New(core.NewState("priority-relay", core.DefaultLimits()), &memLedger{}, deadProber{})
	go e.Run(ctx)
	human, _, err := e.HumanAgent(ctx)
	if err != nil {
		t.Fatal(err)
	}
	feed, detach := e.AttachHumanRelay()
	defer detach()
	sender := registerAgent(t, e, "priority-sender")
	sent, err := e.Do(ctx, &core.Op{
		Kind: core.OpSendMessage, Token: sender, To: human,
		MsgType: core.MsgNotify, Body: "urgent operations alert", RequestPriority: "urgent",
	})
	if err != nil || sent["human_route"] != "relay" {
		t.Fatalf("human send failed before relay: %v %v", sent, err)
	}
	select {
	case notice := <-feed:
		if notice.Priority != "urgent" || notice.Type != core.MsgNotify {
			t.Fatalf("relay lost priority: %+v", notice)
		}
	case <-ctx.Done():
		t.Fatal("urgent notify was not forwarded to relay")
	}
}

func TestLateHumanRelayReceivesUnpostedHighNotifyOnly(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	e := New(core.NewState("late-priority-relay", core.DefaultLimits()), &memLedger{}, deadProber{})
	e.SetHumanNotifier(unavailablePriorityNotifier{})
	go e.Run(ctx)
	human, _, err := e.HumanAgent(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sender := registerAgent(t, e, "priority-sender")
	for _, priority := range []string{"normal", "high"} {
		sent, err := e.Do(ctx, &core.Op{
			Kind: core.OpSendMessage, Token: sender, To: human,
			MsgType: core.MsgNotify, Body: "unposted alert", RequestPriority: priority,
		})
		if err != nil || sent["human_route"] != "none" {
			t.Fatalf("unavailable desktop setup: %v %v", sent, err)
		}
	}
	pending, err := e.PendingForHuman(ctx)
	if err != nil || len(pending) != 1 || pending[0].Priority != "high" {
		t.Fatalf("relay recovery lost high notify or replayed ordinary FYI: %+v %v", pending, err)
	}
}

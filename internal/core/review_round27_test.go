package core

import (
	"testing"
	"time"
)

// R27-1: adopting a mailbox that holds a pending question tells the heir,
// with an event addressed to it, so the wake routes and the inbox
// subscription treat the recovered mail as arrived.
func TestAdoptionAddressesTheHeirForItsRecoveredQuestions(t *testing.T) {
	s := NewState("test", DefaultLimits())
	t0 := time.Now()
	for _, o := range []*Op{
		{Kind: OpRegister, Name: "asker", NewToken: "tok-a", AgentKind: KindPersistent, Nonce: "na", V7Semantics: true},
		{Kind: OpRegister, Name: "lost", NewToken: "tok-l", AgentKind: KindPersistent, Nonce: "nl", V7Semantics: true},
		{Kind: OpRegister, Name: "heir", NewToken: "tok-h", AgentKind: KindPersistent, Nonce: "nh", V7Semantics: true},
		{Kind: OpRegister, Name: "lead", NewToken: "tok-c", AgentKind: KindPersistent, Nonce: "nc", V7Semantics: true},
		{Kind: OpAckBoard, Token: "tok-a"},
		{Kind: OpSendMessage, Token: "tok-a", To: "lost", MsgType: MsgQuestion, Body: "still owed", DeadlineSec: 600, V7Semantics: true},
		{Kind: OpSendMessage, Token: "tok-a", To: "lost", MsgType: MsgNotify, Body: "fyi", V7Semantics: true},
	} {
		if _, _, err := s.Apply(o, t0); err != nil {
			t.Fatal("setup:", err)
		}
	}
	s.Agents["lost"].Status, s.Agents["heir"].Status = StatusDormant, StatusDormant
	s.Agents["lead"].Role = RoleCoordinator
	// On the direct path the heir is the caller unless Space names another
	// agent: the coordinator adopts on the dormant heir's behalf.
	_, evs, err := s.Apply(&Op{Kind: OpAdoptAgent, Token: "tok-c", To: "lost", Space: "heir", V7Semantics: true, AdoptAuthorised: true}, t0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	var questions, notifies int
	for _, ev := range evs {
		if ev.Type == "message.adopted" && ev.To == "heir" {
			switch ev.Data["msg_type"] {
			case MsgQuestion:
				questions++
			case MsgNotify:
				notifies++
			}
		}
	}
	if questions != 1 || notifies != 0 {
		t.Fatalf("adoption emitted %d question event(s) and %d notify event(s) addressed to the heir, "+
			"want 1 and 0: the recovered question wakes nobody and the FYI must not", questions, notifies)
	}
}

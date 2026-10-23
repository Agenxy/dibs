// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package ledger

import (
	"reflect"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

func TestMailboxRepairsReplayMixedHistoricalAndRecordedOps(t *testing.T) {
	led, _ := newLedger(t)
	lim := core.DefaultLimits()
	lim.MaxMailboxDepth = 2
	s := core.NewState("test", lim)
	write := func(op *core.Op) core.Result { return apply(t, s, led, op, t0) }
	for _, name := range []string{"sender", "reader"} {
		write(&core.Op{Kind: core.OpRegister, Name: name, NewToken: name, Nonce: name + "-nonce"})
	}
	for range 2 {
		id := write(&core.Op{Kind: core.OpSendMessage, Token: "sender", To: "reader", MsgType: core.MsgNotify, Body: "old fyi"})["msg_serial"].(uint64)
		write(&core.Op{Kind: core.OpActivityCheckpoint, Token: "reader", NotifyAnnounced: []uint64{id}})
	}
	// The old notify-only replacement still folds as written.
	write(&core.Op{Kind: core.OpSendMessage, Token: "sender", To: "reader", MsgType: core.MsgNotify, Body: "old replacement"})
	// Only new sends apply announced-FYI exemption and every-type replacement.
	var asks []uint64
	for range 2 {
		op := &core.Op{Kind: core.OpSendMessage, Token: "sender", To: "reader", MsgType: core.MsgRequest, Body: "new work", MailboxCapacity: true, QueueDebt: true}
		if err := s.Admit(op); err != nil {
			t.Fatal(err)
		}
		id := write(op)["msg_serial"].(uint64)
		asks = append(asks, id)
		write(&core.Op{Kind: core.OpRespond, Token: "reader", MsgSerial: id, Disposition: "queue", QueueDebt: true})
	}
	bulk := &core.Op{Kind: core.OpAckMessage, Token: "reader", NotifyThroughSerial: s.Serial}
	if err := s.Admit(bulk); err != nil {
		t.Fatal(err)
	}
	write(bulk)
	contact := &core.Op{Kind: core.OpContactEscalate, MsgSerial: asks[0], ContactMailboxFull: true}
	if err := s.Admit(contact); err != nil {
		t.Fatal(err)
	}
	write(contact)
	restored := core.NewState("test", lim)
	if _, err := led.Replay(restored); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s, restored) {
		t.Fatal("live/recorded mailbox state differs from encrypted cold replay")
	}
}

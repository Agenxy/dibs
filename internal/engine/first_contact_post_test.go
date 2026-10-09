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
	"github.com/agenxy/dibs/internal/humanask"
	"github.com/agenxy/dibs/internal/notify"
)

type firstContactDesktop struct {
	detailed bool
	posted   chan humanask.Message
}

func (firstContactDesktop) Available() bool { return true }
func (firstContactDesktop) Presentation() notify.Presentation {
	return notify.Presentation{}
}

func (n firstContactDesktop) Ask(m humanask.Message) (humanask.Answer, error) {
	if n.detailed {
		if m.DeliveryReceipt == nil {
			return humanask.Answer{}, fmt.Errorf("fixture: missing detailed receipt callback")
		}
		m.DeliveryReceipt(notify.ReceiptData{State: "posted"})
	} else {
		if m.Receipt == nil {
			return humanask.Answer{}, fmt.Errorf("fixture: missing legacy receipt callback")
		}
		m.Receipt("posted")
	}
	n.posted <- m
	return humanask.Answer{}, nil
}

// Enter through a published contact escalation, rather than pre-populating the
// ordinary human-send delivery cache: contact can be the first post after boot.
func TestFirstContactPostRecordsBothReceiptFormats(t *testing.T) {
	for _, format := range []string{"legacy", "detailed"} {
		t.Run(format, func(t *testing.T) {
			e := New(core.NewState("first-contact", core.DefaultLimits()), &memLedger{}, deadProber{})
			posted := make(chan humanask.Message, 1)
			e.SetHumanNotifier(firstContactDesktop{format == "detailed", posted})
			ctx, cancel := context.WithCancel(context.Background())
			joined := make(chan struct{})
			go func() { e.Run(ctx); close(joined) }()
			t.Cleanup(func() { cancel(); <-joined })
			do := func(op *core.Op) core.Result {
				t.Helper()
				r, err := e.Do(ctx, op)
				if err != nil {
					t.Fatalf("setup %s: %v", op.Kind, err)
				}
				return r
			}
			sender := do(&core.Op{Kind: core.OpRegister, Name: "sender"})["token"].(string)
			do(&core.Op{Kind: core.OpRegister, Name: "worker"})
			e.humanDelivery.mu.Lock()
			fresh := e.humanDelivery.bySerial == nil
			e.humanDelivery.mu.Unlock()
			if !fresh {
				t.Fatal("setup populated delivery records before the first human post")
			}
			const body = "private first-contact request body"
			serial := do(&core.Op{Kind: core.OpSendMessage, Token: sender, To: "worker", MsgType: core.MsgRequest, Body: body})["msg_serial"].(uint64)
			contact := do(&core.Op{Kind: core.OpContactEscalate, MsgSerial: serial})["contact_serial"].(uint64)
			select {
			case m := <-posted:
				if m.Type != "contact" || m.Serial != contact || m.Contact == nil || m.Contact.Message != serial || strings.Contains(m.Body, body) {
					t.Fatalf("first post lost its contact identity or exposed mail: %+v", m)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("first contact receipt did not complete")
			}
			r, err := e.query(ctx, func() core.Result {
				c := e.state.Contacts[contact]
				if c == nil {
					return nil
				}
				return core.Result{"notified": !c.NotifiedAt.IsZero(), "resolved": !c.ResolvedAt.IsZero(), "mail_state": e.state.Messages[serial].State}
			})
			if err != nil {
				t.Fatal(err)
			}
			if r["notified"] != true || r["resolved"] != false || r["mail_state"] != core.MsgStatePending {
				t.Fatalf("posting did not record the receipt while preserving unread mail: %+v", r)
			}
			e.humanDelivery.mu.Lock()
			d := e.humanDelivery.bySerial[contact]
			e.humanDelivery.mu.Unlock()
			if !d.Posted || d.State != "posted" {
				t.Fatalf("first post lost its delivery evidence: %+v", d)
			}
		})
	}
}

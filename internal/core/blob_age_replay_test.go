// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package core

import (
	"testing"
	"time"
)

// A ledger written before the attachment age bound existed has to fold the
// same way after it. v0.0.18 kept a referenced attachment for as long as the
// mail naming it lived, so an agent could attach the same blob again after a
// week and the send was ledgered. Replaying that tail on a build that
// age-evicts during the sweep before it drops the blob, refuses the later
// send, and the daemon will not boot on its own ledger: the bound is a rule
// in Apply, and a rule in Apply is retroactive.
func TestABlobAgeBoundDoesNotRewriteALedgerWrittenWithoutOne(t *testing.T) {
	ops := func(s *State) error {
		ackReg(t, s, "alpha", "ta", t0)
		ackReg(t, s, "beta", "tb", t0)
		id := putBlob(t, s, "ta", "diagram", t0)["blob"].(string)
		send := func(opID string, at time.Time) error {
			_, _, err := s.Apply(&Op{
				Kind: OpSendMessage, Token: "ta", To: "beta", OpID: opID,
				MsgType: MsgNotify, Body: "see attached", Attachments: []Attachment{{Blob: id}},
			}, at)
			return err
		}
		if err := send("first", t0); err != nil {
			return err
		}
		if _, _, err := s.Apply(&Op{Kind: OpSweep}, t0.Add(8*24*time.Hour)); err != nil {
			return err
		}
		return send("again", t0.Add(8*24*time.Hour+time.Minute))
	}

	before := DefaultLimits()
	before.BlobMaxAge = 0 // what every op in a v0.0.18 ledger was applied under
	if err := ops(NewState("n1", before)); err != nil {
		t.Fatalf("setup: the history must be valid without an age bound: %v", err)
	}
	if err := ops(NewState("n1", DefaultLimits())); err != nil {
		t.Fatalf("replaying that history with today's default limits: %v", err)
	}
}

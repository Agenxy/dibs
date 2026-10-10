// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package core

import (
	"encoding/json"
	"fmt"
	"testing"
)

// Enter through the recorded JSON, not a hand-set page flag. An empty list
// must survive omitempty; dropping it would replay as delivery of all mail.
func TestMailboxCheckpointWirePreservesEmptyAndSelectedDelivery(t *testing.T) {
	for _, selection := range []string{"[]", "[5]"} {
		t.Run(selection, func(t *testing.T) {
			st := NewState("mailbox", DefaultLimits())
			apply := func(op *Op) {
				t.Helper()
				if _, _, err := st.Apply(op, t0); err != nil {
					t.Fatal(err)
				}
			}
			for _, id := range []string{"sender", "reader"} {
				apply(&Op{Kind: OpRegister, Name: id, NewToken: id, Nonce: id})
				apply(&Op{Kind: OpAckBoard, Token: id})
			}
			for range 3 {
				apply(&Op{Kind: OpSendMessage, Token: "sender", To: "reader", MsgType: MsgNotify, Body: "retained"})
			}
			var op Op
			wire := fmt.Sprintf(`{"kind":"activity_checkpoint","agent_id":"reader","mailbox_serials":%s}`, selection)
			if err := json.Unmarshal([]byte(wire), &op); err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(op)
			if err != nil {
				t.Fatal(err)
			}
			if err = json.Unmarshal(raw, &op); err != nil {
				t.Fatal(err)
			}
			apply(&op)
			for id := uint64(5); id <= 7; id++ {
				want := MsgStatePending
				if selection == "[5]" && id == 5 {
					want = MsgStateDelivered
				}
				if m := st.Messages[id]; m == nil || m.State != want {
					t.Fatalf("wire replay changed omitted message %d: %v", id, m)
				}
			}
			// The old absent-selection checkpoint retains read-all semantics.
			apply(&Op{Kind: OpAckBoard, Token: "reader"})
			for id := uint64(5); id <= 7; id++ {
				if st.Messages[id].State != MsgStateDelivered {
					t.Fatal("historical checkpoint changed")
				}
			}
		})
	}
}

// This exact serialized-op test also runs against the pre-change source. The
// old fold may forget the selected page's new awareness receipt, but must boot,
// advance every recorded serial, leave omitted mail untouched and preserve the
// ordinary consumed-notify retention transition.
func TestMailboxCarrierRollbackFoldRemainsHarmless(t *testing.T) {
	st := NewState("rollback", DefaultLimits())
	apply := func(raw string) {
		t.Helper()
		var op Op
		if err := json.Unmarshal([]byte(raw), &op); err != nil {
			t.Fatal(err)
		}
		before := st.Serial
		if _, _, err := st.Apply(&op, t0); err != nil || st.Serial != before+1 {
			t.Fatalf("serialized rollback fold refused or lost a serial: %s (%v)", raw, err)
		}
	}
	for _, raw := range []string{
		`{"kind":"register","name":"sender","token":"sender"}`,
		`{"kind":"register","name":"reader","token":"reader"}`,
		`{"kind":"send","agent_id":"sender","to":"reader","msg_type":"notify","body":"recent complete FYI"}`,
		`{"kind":"send","agent_id":"sender","to":"reader","msg_type":"notify","body":"omitted FYI"}`,
		`{"kind":"activity_checkpoint","agent_id":"reader","mailbox_serials":[3]}`,
		`{"kind":"activity_checkpoint","agent_id":"reader","mailbox_serials":[]}`,
		`{"kind":"activity_checkpoint","agent_id":"reader","notify_announced":[4]}`,
		`{"kind":"ack","agent_id":"reader","msg_serial":3,"notify_consumption":"presented"}`,
	} {
		apply(raw)
	}
	if m := st.Messages[4]; m.State != MsgStatePending || m.DeliveredAt != 0 || m.Consumed {
		t.Fatalf("rollback carrier spent omitted mail: %+v", m)
	}
	if m := st.Messages[3]; !m.Consumed || m.State != MsgStateAcked || m.AckedAt != 8 || !m.TerminalAt.Equal(t0) || m.Body != "recent complete FYI" {
		t.Fatalf("rollback lost ordinary consumption/retention semantics: %+v", m)
	}
}

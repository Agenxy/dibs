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
			wire := fmt.Sprintf(`{"kind":"check_in_page","agent_id":"reader","mailbox_serials":%s}`, selection)
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

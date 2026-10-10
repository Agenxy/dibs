// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package core

import (
	"testing"
	"time"
)

func TestContactCheckpointAdmitsOnlyOwnMonotonicPrefix(t *testing.T) {
	st := NewState("contact-read", DefaultLimits())
	for i, name := range []string{"reader", "peer"} {
		if _, _, err := st.Apply(&Op{Kind: OpRegister, Name: name, NewToken: name + "-token"}, time.Unix(int64(i), 0)); err != nil {
			t.Fatal(err)
		}
	}
	st.Agents["reader"].ContactNoticeReadAt = 2
	for _, tc := range []struct {
		name string
		op   Op
	}{
		{"future", Op{Kind: OpActivityCheckpoint, Token: "reader-token", ContactNoticeThroughSerial: 3}},
		{"regression", Op{Kind: OpActivityCheckpoint, Token: "reader-token", ContactNoticeThroughSerial: 1}},
		{"other actor", Op{Kind: OpActivityCheckpoint, Token: "reader-token", AgentID: "peer", ContactNoticeThroughSerial: 2}},
		{"before incarnation", Op{Kind: OpActivityCheckpoint, Token: "peer-token", ContactNoticeThroughSerial: 1}},
		{"wrong kind", Op{Kind: OpOutcomeRead, Token: "reader-token", ContactNoticeThroughSerial: 2}},
		{"envelope", Op{Kind: OpActivityCheckpoint, Token: "reader-token", ContactNoticeThroughSerial: 2, MsgSerial: 1}},
		{"missing authentication", Op{Kind: OpActivityCheckpoint, ContactNoticeThroughSerial: 2}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := st.Admit(&tc.op); err == nil {
				t.Fatal("Admit accepted invalid contact prefix")
			}
		})
	}
	if err := st.Admit(&Op{Kind: OpActivityCheckpoint, Token: "reader-token", ContactNoticeThroughSerial: 2}); err != nil {
		t.Fatal("idempotent own read refused:", err)
	}
}

func TestContactCheckpointChangesSerialOnlyForNewRead(t *testing.T) {
	st := NewState("contact-read", DefaultLimits())
	now := time.Unix(1700000000, 0)
	if _, _, err := st.Apply(&Op{Kind: OpRegister, Name: "reader", NewToken: "reader-token"}, now); err != nil {
		t.Fatal(err)
	}
	op := &Op{Kind: OpActivityCheckpoint, Token: "reader-token", ContactNoticeThroughSerial: 1}
	if err := st.Admit(op); err != nil {
		t.Fatal(err)
	}
	before := st.Serial
	if _, ev, err := st.Apply(op, now.Add(time.Second)); err != nil || ev == nil || st.Serial != before+1 || st.Agents["reader"].ContactNoticeReadAt != 1 {
		t.Fatalf("new prefix did not advance exactly once: %v %v %d", ev, err, st.Serial)
	}
	before = st.Serial
	coordination := st.Agents["reader"].LastCoordination
	if _, ev, err := st.Apply(op, now.Add(2*time.Second)); err != nil || ev != nil || st.Serial != before || !st.Agents["reader"].LastCoordination.Equal(coordination) {
		t.Fatalf("duplicate read changed replayable state: %v %v %d", ev, err, st.Serial)
	}
	// The historical absent-field checkpoint still advances its activity
	// record, even though this incarnation already read a contact notice.
	if _, ev, err := st.Apply(&Op{Kind: OpActivityCheckpoint, Token: "reader-token"}, now.Add(3*time.Second)); err != nil || ev == nil || st.Serial != before+1 {
		t.Fatalf("historical checkpoint effect changed: %v %v %d", ev, err, st.Serial)
	}
}

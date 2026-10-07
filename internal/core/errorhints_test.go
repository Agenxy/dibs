// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package core

import (
	"errors"
	"testing"
	"time"
)

func TestDynamicErrorHintsNameCorrectiveCalls(t *testing.T) {
	s := NewState("error-hints", DefaultLimits())
	if _, _, err := s.Apply(&Op{Kind: OpRegister, Name: "worker", NewToken: "worker"}, time.Now()); err != nil {
		t.Fatal("setup:", err)
	}
	for _, tc := range []struct{ kind, code string }{
		{"unknown-operation", "E_BAD_OP"}, {OpAckMessage, "E_NO_MESSAGE"}, {OpOutcomeRead, "E_NO_MESSAGE"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			_, _, err := s.Apply(&Op{Kind: tc.kind, Token: "worker", MsgSerial: 999}, time.Now())
			var ce *Error
			if !errors.As(err, &ce) || ce.Code != tc.code || ce.Hint == "" {
				t.Fatalf("wrong/empty corrective error: %v", err)
			}
		})
	}
	for _, e := range []*Error{ErrNoMessage(10, 0), ErrNoMessage(10, 20), ErrNotYourMessage(10, "sender", "receiver"), ErrWrongKind(10, "space")} {
		if e.Hint == "" {
			t.Fatalf("dynamic helper omitted hint: %v", e)
		}
	}
}

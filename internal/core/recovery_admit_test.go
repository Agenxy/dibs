// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package core

import (
	"strings"
	"testing"
)

// A retained credential group is an alternative to a single nonce, never an
// unbounded or ambiguous second way to authenticate one registration.
func TestRecoveryNonceAdmissionRejectsAmbiguousGroups(t *testing.T) {
	lim := DefaultLimits()
	valid := func() *Op {
		return &Op{Kind: OpRegister, Name: "worker", RecoveryNonces: []string{"first-retained", "second-retained"}}
	}
	tests := []struct {
		name string
		op   *Op
		bad  bool
	}{
		{"valid retained group", valid(), false},
		{"wrong operation", &Op{Kind: OpUpdate, Name: "worker", RecoveryNonces: valid().RecoveryNonces}, true},
		{"explicit nonce too", &Op{Kind: OpRegister, Name: "worker", Nonce: "another", RecoveryNonces: valid().RecoveryNonces}, true},
		{"missing name", &Op{Kind: OpRegister, RecoveryNonces: valid().RecoveryNonces}, true},
		{"one candidate", &Op{Kind: OpRegister, Name: "worker", RecoveryNonces: []string{"only"}}, true},
		{"too many candidates", &Op{Kind: OpRegister, Name: "worker", RecoveryNonces: make([]string, MaxRecoveryNonces+1)}, true},
		{"oversized candidate", &Op{Kind: OpRegister, Name: "worker", RecoveryNonces: []string{strings.Repeat("x", lim.MaxIDBytes+1), "second"}}, true},
		{"duplicate candidate", &Op{Kind: OpRegister, Name: "worker", RecoveryNonces: []string{"same", "same"}}, true},
		{"blank candidate", &Op{Kind: OpRegister, Name: "worker", RecoveryNonces: []string{"first", "  "}}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := Admit(tc.op, lim)
			if tc.bad && err == nil {
				t.Fatal("ambiguous retained credential group admitted")
			}
			if !tc.bad && err != nil {
				t.Fatalf("valid retained credential group refused: %v", err)
			}
		})
	}
}

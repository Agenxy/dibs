// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package core

import (
	"errors"
	"strings"
	"testing"
)

func TestRecoveryAdmissionRequiresSelfDescribingFenceShape(t *testing.T) {
	legacy := "sha256:" + strings.Repeat("a", 64)
	for _, tc := range []struct {
		name, fence string
		valid       bool
	}{
		{"v1", legacy, true},
		{"v2", "v2:" + legacy, true},
		{"empty", "", false},
		{"short", legacy[:70], false},
		{"long", legacy + "a", false},
		{"v2-short", "v2:" + legacy[:70], false},
		{"v2-long", "v2:" + legacy + "a", false},
		{"unknown-version", "v3:" + legacy, false},
		{"wrong-hash", "sha512:" + strings.Repeat("a", 64), false},
		{"wrong-v2-hash", "v2:sha512:" + strings.Repeat("a", 64), false},
		{"non-hex", "sha256:" + strings.Repeat("g", 64), false},
		{"upper-hex", "sha256:" + strings.Repeat("A", 64), false},
		{"nested-prefix", "v2:v2:" + legacy, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			op := &Op{Kind: OpSendMessage, MsgType: MsgRequest, Recovery: &RecoveryRequest{From: "source", Into: "survivor", SourceCreated: 1, SurvivorCreated: 2, CoordinatorCreated: 3, Fence: tc.fence}}
			err := admitRecoveryRequest(op)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%t: %v", tc.valid, err)
			}
			if err != nil {
				var e *Error
				if !errors.As(err, &e) || e.Code != "E_BAD_ARG" || e.Hint == "" {
					t.Fatalf("malformed fence has no corrective refusal: %v", err)
				}
			}
		})
	}
}
